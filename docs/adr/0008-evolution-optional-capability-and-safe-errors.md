# ADR-0008 — Verified Evolution capabilities and opaque client errors

- Status: Accepted (2026-10-11; design-reviewed and accepted)
- Scope: Evolution client only; no live service, schema, frontend or bridge route changes.
- Design: Evolution client hardening.

## Context

Independent QA and adversarial review of `b6aa119` reproduced a HIGH credential
leak through a successful status response containing an overflowing JSON number;
the raw decoder error revealed the JSON key/credential. Public HTTP error body
snippets can also expose customer data through error serialization. The client
paired published GO optional paths with **legacy request bodies**: a configurable
path cannot make that request shape valid. The legacy bridge exposed optional
mark-read/download calls but the preserved source has no proven working deployed
GO behavior for these shapes. Provider idempotent replay is unverified.

## Decision and approved behavior changes

1. Across *all* provider decode boundaries return a fixed, opaque category;
   never retain upstream text, JSON keys/numbers or inspectable raw decoder
   causes in exported error objects or error chains. Non-2xx retains numeric
   status and fixed operation/category only; remove `HTTPError.Body` and any
   equivalent excerpt. Cancellation/deadline classification remains accessible
   without leaking URLs. No external payload or configured path in error logs.
2. Keep mark-read optional, but use the source-proven GO path **and** GO
   `{id:[messageID],number:chatJID}` body. Reject unsupported custom paths
   before network I/O. A 404 does not prove that the endpoint is absent.
3. Disable media download (including custom paths) without network I/O. The
   current ID-only inputs cannot construct GO's published full-message request;
   no production caller was proven by the legacy audit. Prefer webhook media.
   Re-enablement needs verified source contract, media limits and tests.
4. Finite bridge-owned byte budgets: outbound encoded JSON 1 MiB, ordinary
   response 4 MiB, error response 16 KiB; test exact boundaries with limit+1
   read. These are local safety limits, not GO provider limits. Unknown outcome
   after any attempted send must never automatically retry, even with a caller
   supplied ID. Reject oversized requests *before* I/O.
5. Preserve valid legacy ID/MIME alias precedence; malformed conflicting aliases
   fail closed. Emit only fixed-operation, status, duration, fixed-category
   diagnostic metadata; omit private fields and untrusted correlation values.

These deliberately replace unsafe legacy tolerance and contract assumptions.
The changed optional behavior is **approved** because GO route/body mismatch was
source-proven while legacy success on the deployed GO build was not; claiming
download works would manufacture capability. Keep `[needs live validation]` for
actual GO 0.7.2 deployment. No provider-side exactly-once/idempotency is assumed.

## Gate

Implement the approved design with synthetic source-shape `httptest`
regressions, byte-budget tests, opaque error-chain/serialization assertions and
one-attempt-after-unknown-send tests. Run Go test + race; route the immutable
commit through independent QA and code review. A skipped optional capability
cannot be reported as working parity or a passing engine gate. No
production traffic or destructive operations are authorized by this decision.
