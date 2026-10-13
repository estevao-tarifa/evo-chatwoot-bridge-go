## Summary

<!-- What changed and why (not how). Cite the ADR it implements (`docs/adr/…`) or the accepted issue. -->

## Architectural decisions

<!-- Any boundary, dependency-direction or data-flow change: link its ADR in docs/adr/. Write "None" if there is none. -->

## Files changed

<!-- The files that changed, grouped by intent. -->

## Tests executed

<!--
"Tests pass" without the command is not evidence. Paste the exact command you ran
and its observed result. CI runs, from backend/: gofmt -l . && go vet ./... &&
go build ./... && go test -race -shuffle=on ./... && go mod verify; from frontend/:
npm ci && npm run typecheck && npm test && npm run build.
A claim that could not be verified in your environment must be labelled [needs live validation].
-->

```
$ <command>
<observed result>
```

## Problems and fixes

<!-- Anything that blocked the work and how it was resolved. -->

## Audit result

<!-- The review verdict: severity (CRITICAL/HIGH/MEDIUM/LOW) and any finding id. -->

## Remaining risks

<!-- Known gaps, follow-ups, or "None". -->

---

## Checklist (derived from CONTRIBUTING.md)

- [ ] A spec exists for this change (a written spec or an issue the maintainer accepted); nothing merged without one.
- [ ] If a boundary, dependency direction or data flow changed, an ADR in `docs/adr/` records it and no accepted ADR is contradicted.
- [ ] The Go skills loaded for this change are named here (e.g. "loaded: golang-code-style, golang-testing"); whoever touched the engine, persistence or observability layer, and the QA pass, names theirs.
- [ ] The exact test command **and** its observed result are pasted above; any claim that could not be verified is labelled `[needs live validation]`.
- [ ] Migrations are additive, forward-only and idempotent: new `backend/migrations/NNNN_snake_case.sql` only, no edit/renumber of a released migration, no `DROP`/`DELETE`/`UPDATE` of existing data, legacy bookkeeping table untouched.
- [ ] No secret, token, phone number or customer name anywhere in the diff, the commit, the PR or the pasted logs — including webhook payloads, `instanceToken`, `apikey`/`api_access_token`, cookies, `.env`, `DATABASE_URL`; synthetic data only. No secret is logged.
- [ ] No existing regression test was weakened (identity reuse, dedup, loop-break, retry/quarantine, media types, CORS, OpenAPI coverage).
- [ ] Docs updated: `docs/API.md` on a route/payload change, the ADR on a decision change, `deploy/README.md` on an operator step, `CHANGELOG.md` under `## [Unreleased]`, OpenAPI (`backend/api/openapi.yaml`) consistent.
- [ ] One small, focused Conventional Commit; clean working tree.

## AI-agent disclosure

<!-- Required when an AI agent produced any part of this change. -->

- Agent(s) used:
- Model(s) used:
- QA pass performed by a **different** agent than the author (name it), per CONTRIBUTING §1.6:
- Independent review pass performed by a **different** agent than the author (name it):
