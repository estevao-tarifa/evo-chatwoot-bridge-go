# AGENTS.md — rules for AI agents working on this repository

This file is the contract for any AI coding agent that touches this codebase. It
is not a replacement for [CONTRIBUTING.md](CONTRIBUTING.md) (the process for
humans) or [docs/SKILLS.md](docs/SKILLS.md) (the Go skills to load); it adds the
rules that exist because an agent, not a person, is doing the work.

References like **§7** and **§12** elsewhere in `docs/` point at this file.

## 1. What this project is

A bidirectional bridge between **Evolution GO** (WhatsApp) and **Chatwoot**:
Go backend (single binary, no Node/Vite at runtime), a React admin SPA deployed
independently, and the existing MySQL schema preserved with incremental
forward-only migrations.

## 2. Source of truth

- `docs/ARCHITECTURE.md`, `docs/API.md`, `docs/bridge/ROUTING_MATRIX.md` and the
  ADRs in `docs/adr/` are authoritative. Do not contradict an accepted ADR; write
  a new one instead.
- Behaviour is preserved **only when a test proves it**. A behavioural change
  needs an ADR and the maintainer's approval.

## 3. Mandatory workflow

```
SPEC → ARCHITECTURE → CONTRACT → IMPLEMENTATION → TEST → REVIEW → DOCS → COMMIT
```

No implementation starts before the spec exists. No change advances without an
independent QA pass and an independent review — **an agent different from the one
that wrote the code** — and a small Conventional Commit.

## 4. Definition of Done

Implementation + tests **executed and reported** (exact command and observed
result) + docs updated + OpenAPI consistent (if an endpoint changed) + clean
working tree + one Conventional Commit.

## 5. Autonomy and hard limits

- Never run a destructive operation on a database or a host: no `DROP`, no
  data deletion, no `rm -rf`, no `reset --hard`, no force push. Migrations are
  forward-only. "Additive yes, delete no."
- Never touch a production environment: no deploy, no restart, no webhook
  re-point, unless the maintainer has explicitly authorized that specific action
  in the current session.
- Never read or write a secret file, and never print a secret value.

## 6. Testing rules

- Run the tests; report the command **and** its result. "Tests pass" without the
  command is not evidence.
- A new permanent test must catch a plausible, consumer-visible defect (behaviour,
  boundary, invariant, ordering, error path). Do not add tests for wording,
  wiring, mock echoes or source text.
- The `storage/mysql` integration suite needs a database whose name ends in
  `_test` and, on slow hosts, `-timeout 30m`.

## 7. Identity integrity invariants — the reason this rewrite exists

An **external** message (Evolution direct, WhatsApp Web, another automation,
another client) must never, by itself, create a duplicate contact or conversation
when the correct identity already exists. Any change that touches resolution must
satisfy all of these:

1. An externally sent message must not, by itself, create a new contact or
   conversation when the correct identity already exists.
2. Reuse the correct mapping **before** creating any Chatwoot entity.
3. A group's identity is a **stable JID**, scoped by connection and account —
   never the display name.
4. Group name and contact name are **different** attributes; never overwrite
   blindly, and never with `"Grupo"`, a raw phone number or a JID.
5. The conversation title follows the real Chatwoot/domain rules — never a JID.
6. Messages originated by the bridge are recognised and never re-sent in a loop.
7. A conversation is created only when the routing policy requires it.
8. Reuse respects connection, inbox, contact identity, group identity and state.

## 8. Idempotency and delivery

- Dedup is deterministic and persistent; two workers must never process the same
  message. Claim before acknowledging.
- A send whose outcome is unknown is **quarantined**, never retried blindly: never
  deliver twice to buy a lower loss rate.
- Never introduce an in-memory cache as the only guard for a correctness
  invariant.

## 9. Test data and secrets

- Use **synthetic** fixtures only. Never a real phone number, group name, message
  body, token or customer name — in code, tests, fixtures, docs or commit
  messages.
- Webhook payloads carry `instanceToken`; treat a payload as secret material and
  sanitise before persisting or logging.
- Never log tokens, auth headers, cookies, media base64, full payloads or private
  message content.

## 10. Security surface

- Webhook ingress fails closed: a wrong or missing token is `401`, an unset secret
  is `503`.
- Admin routes require a session; mutations require the CSRF header and a JSON
  content type. Never reproduce a public/fallback admin procedure.
- The backend never serves frontend assets (ADR-0007).

## 11. Dependencies

Direct dependencies are an allowlist enforced in CI (ADR-0003). A new runtime or
test dependency needs an ADR update or an explicit justification accepted by the
maintainer. Prefer the standard library.

## 12. Go skills (load them explicitly — mandatory)

A Go skills pack (`.agents/skills/`) may be available in your environment. Skills
are **not applied automatically**; load the relevant ones before writing or
reviewing Go, and **name the skills you loaded** in your report. If you are
unsure which apply, start with `golang-how-to`. The task-to-skill matrix is in
[docs/SKILLS.md](docs/SKILLS.md) §2. This rule applies to every agent that writes
or reviews Go code, including a stand-in general-purpose agent.

## 13. Report format

When you hand work back: what changed (files), the exact verification command and
observed result, the skills you loaded, anything you could not verify, and the
commit. Disclose the agent and model that produced the change.
