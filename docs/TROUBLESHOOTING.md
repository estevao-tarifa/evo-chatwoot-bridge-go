# Troubleshooting — evo-chatwoot-bridge (Go)

[README](../README.md) · [Install](INSTALL.md) · [Configuration](CONFIGURATION.md)

Every entry here is a failure mode seen in real deployments, with the cause and
the fix. Nothing in this file requires reading the source.

## Fast triage

```sh
BRIDGE=https://bridge.example.com

curl -fsS "$BRIDGE/healthz"      # process alive?
curl -fsS "$BRIDGE/readyz"       # database reachable? {"status":"unavailable","reason":...}
curl -fsS "$BRIDGE/api/v1/meta"  # name, version, commit

# authenticated (log in first, keep cookies.txt)
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/test" -H 'X-CSRF: 1'
curl -sS -b cookies.txt "$BRIDGE/api/v1/connections/$ID/webhook-status"
curl -sS -b cookies.txt "$BRIDGE/api/v1/logs?connectionId=$ID&limit=20"
```

`/readyz` answers `unavailable` with a `reason` — read that first, it is exact.
`webhook-status` tells you whether each platform is actually pointed here.
`logs` shows the per-message outcome with `direction` and `status`.

---

## The server exits during boot

**Cause.** `APP_ENV=production` (the default) makes `WEBHOOK_TOKEN_SECRET` and
`SESSION_SECRET` mandatory, and every set-but-invalid value fails fast: a
wildcard in `CORS_ALLOWED_ORIGINS`, a `BRIDGE_URL` with a trailing slash or a
query, `COOKIE_SAMESITE=none` without `COOKIE_SECURE=true`, a `DATABASE_URL` with
unsupported query parameters.

**Fix.** Read the first line of the log — it names the offending variable. This
is deliberate: a half-configured bridge is worse than a bridge that does not
start.

---

## `/readyz` says unavailable

**Cause.** The MySQL pool cannot be opened or pinged: wrong host/port, wrong
credentials, or the database does not exist.

> Note what `/readyz` does **not** check: the schema. It pings MySQL only, so an
> unmigrated database still reports `200` — the missing tables instead surface as
> a refused webhook (see [Webhook answers 503](#webhook-answers-503)).

**Fix.**

```sh
docker compose -f deploy/docker-compose.prod.yml run --rm bridge migrate
curl -fsS http://127.0.0.1:8100/readyz
```

If the migration itself fails, the error names the table. A legacy database
without `processed_messages` is *expected* before the first migration.

---

## Webhook answers 401

**Symptom.** Evolution or Chatwoot logs a delivery failure; the bridge log shows
an authentication rejection on `/api/webhooks/...`.

**Cause, in order of likelihood**

1. `WEBHOOK_TOKEN_SECRET` was rotated and the platform still posts to the **old**
   derived URL.
2. The URL was copied by hand and the token is truncated or the connection id in
   the path is wrong (the token is derived **from that id**).
3. `CHATWOOT_WEBHOOK_SECRET` is set but Chatwoot does not sign the payload — the
   route then fails closed.

**Fix.** Re-register instead of copying:

```sh
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/webhook" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' -d '{}'
curl -sS -b cookies.txt "$BRIDGE/api/v1/connections/$ID/webhook-status"
```

The token URLs are only ever returned by
`…/webhook-url/evolution` and `…/webhook-url/chatwoot`.

---

## Webhook answers 503

**Cause.** `WEBHOOK_TOKEN_SECRET` is unset, so no token can be validated. The
ingress refuses rather than accepting unauthenticated traffic.

**Fix.** Set the secret and restart. If you generate a new one, re-run the
webhook registration on every connection.

---

## WhatsApp → Chatwoot stopped working

**Symptom.** Messages appear on the phone but no row is added to `message_logs`.

**Cause**

1. The Evolution instance is disconnected (`connected=false`).
2. The Evolution webhook points somewhere else, or was reset by an instance
   reconnect.
3. The `subscribe` list was replaced with **v2** event names. Evolution GO 0.7.2
   accepts the v1 names (`MESSAGE`, `SEND_MESSAGE`, …); some builds silently
   ignore v2 names and then deliver nothing, with no error anywhere.

**Fix.**

```sh
curl -sS -X GET "$EVO_URL/instance/status" -H "apikey: $EVOLUTION_API_KEY"
curl -sS -b cookies.txt "$BRIDGE/api/v1/connections/$ID/webhook-status"
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/webhook" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' -d '{}'   # re-asserts both webhooks
```

The exact accepted list is in [INSTALL §4.1](INSTALL.md#41-if-you-wire-the-platforms-by-hand).
Reconnects can reset the webhook registration, so re-assert it after any
`logout`/`connect` cycle.

---

## Chatwoot → WhatsApp stopped working

**Symptom.** The agent's reply stays in Chatwoot; `processed_messages` shows
`failed` with `error_code=no_mapping`, or the bridge logs
`chatwoot fail-attend, reason: "no contacts_map row"`.

**Cause.** The conversation has no stored identity mapping. This was a real
defect (the resolved conversation id was not persisted) and is fixed; a mapping
written by an older build can still be missing.

**Fix.** Update to a current build. For an existing unmapped conversation, make
the identity known again by receiving one inbound WhatsApp message for that
contact — the bridge resolves the conversation and persists the mapping, after
which agent replies route to WhatsApp.

---

## Messages stuck in quarantine

**Symptom.** A row with `status='quarantined'` in `processed_messages`, and a log
line for the same message id.

**Cause.** This is **by design**. When a send's outcome cannot be correlated (no
message id came back), the bridge refuses to retry blindly: a blind retry could
deliver the same message twice on WhatsApp. Quarantine keeps rule "never send
twice" above "never lose one".

**What to do.** Look up the message on the upstream side. If it was **not**
delivered, resend it from the origin — the bridge will treat the resend as a new
operation. If it **was** delivered, nothing is needed. There is no automatic
un-quarantine, intentionally.

---

## A duplicate message appears on WhatsApp or Chatwoot

**Cause.** Either two bridge replicas are running, or a webhook was delivered
twice and the claim was bypassed.

**Check.**

```sql
SELECT external_id, direction, status, attempt, updated_at
FROM processed_messages
WHERE external_id IN ('<id1>','<id2>') ORDER BY updated_at;
```

A single claim row per message is the invariant. If you run more than one
replica, stop: the cross-replica uniqueness index is still on the roadmap (see
[README §Project status](../README.md#project-status-and-known-limitations)).

---

## The SPA returns 404 on `/api/api/v1/...`

**Cause.** `BRIDGE_API_BASE_URL` ends with `/api`. The SPA's own routes already
include `/api/v1`.

**Fix.** Set it to the bare origin (`https://bridge.example.com`), then restart the
UI container.

## The SPA logs out immediately, or login does nothing

**Cause.** Cross-origin session cookie: the browser refuses a `SameSite=Lax`
cookie on a cross-site request, and Chrome drops `SameSite=None` without
`Secure`.

**Fix.** Serve the SPA **same-origin** (one domain, `/` → UI and `/api` → bridge),
which is what EasyPanel's two-domain routing gives you. If you must split origins,
set `CORS_ALLOWED_ORIGINS` to the UI origin and `COOKIE_SAMESITE=none` with
`COOKIE_SECURE=true`.

## `instanceStatus` shows `unknown`

**Cause.** Cosmetic. Evolution GO 0.7.2 answers `GET /instance/status` with
`{"data":{"Connected":true,…}}`; the mapper only looks for
`state`/`status`/`connectionStatus`. Messaging is unaffected.

---

## A group is not created, or its name is unchanged

**Cause.** Both are the identity invariants, not bugs: a group is keyed by its
**stable JID**, and its name is taken only from an authoritative
`groupData.name`. A group whose only available name is the placeholder `"Grupo"`
is **never** created, and an existing name is never overwritten by a payload that
lacks `groupData`.

**What to check.** The event really is a group event (`IsGroup=true` and a
`@g.us` chat), and `ignoreGroups` is `false` for that connection if you want
groups routed at all.

## Events arrive but nothing is written to the database

**Cause.** The event allow-list. Presence, receipts, labels, connection and
similar events are acknowledged with `200` and discarded **before** any write —
by design, so noise never creates entities.

**Fix.** Nothing for those events. If a *message* event is being discarded, it is
a private note, an agent bot message, or a message type the routing matrix
excludes; the log line names the reason.

---

## The webhook returns 200 but the queue is full

**Cause.** `QUEUE_CAPACITY`/`QUEUE_MAX_BYTES` reached under a burst (typically a
slow Chatwoot). The bridge rejects instead of dropping silently.

**Fix.** Raise the capacity, and check upstream latency first — writes to
Chatwoot have been measured above 8 s on shared hosts.

---

## Tests: `storage/mysql` times out

**Symptom.** `go test ./internal/storage/mysql/...` hits the 10-minute default
timeout on a shared host (the suite churns schema per case).

**Fix.**

```sh
TEST_MYSQL_DSN='user:pass@127.0.0.1:3306/evo_bridge_test' \
  go test -count=1 -p 1 -timeout 30m ./internal/storage/mysql/...
```

The integration suite requires a database whose name ends in `_test`.
