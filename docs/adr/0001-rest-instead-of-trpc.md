# ADR-0001 — REST/JSON API instead of tRPC

- Status: Accepted
- Date: 2026-10-10
- Related: ADR-0006 (auth), ADR-0007 (split deployments)

## Context

The legacy system (v5.4.4) is a TypeScript monolith: the React SPA talks to the
backend through tRPC procedures (28 in use, inventoried in the rewrite
assessment), sharing types end-to-end. The backend is rewritten in Go, and no
production-grade tRPC server exists for Go — the protocol is a TS client/server
implementation detail, not a wire standard. `AGENTS.md` mandates a REST/JSON API
and independent frontend deployment. The frontend is kept (React 19 + Vite) and
adapted, not rewritten visually.

## Decision

1. The backend exposes a hand-documented **REST/JSON API** consumed by the SPA,
   curl and future integrations.
2. Route layout: admin/session routes under `/api/*`; webhook ingress under
   `/api/webhooks/*` (separate trust boundary, ADR-0006); ops endpoints
   `/healthz`, `/readyz`, `/metrics`.
3. **Admin API is versioned: `/api/v1/*`.** Because the frontend and backend
   deploy independently, the prefix lets the contract evolve without breaking an
   already-deployed frontend. Webhooks (`/api/webhooks/*`, pinned in external
   configuration) and ops endpoints (`/healthz`, `/readyz`, `/metrics`) stay
   unversioned. Removing/renaming fields still requires a new ADR.
   (Amended 2026-10-10 by the maintainer after the foundation slice; the
   original text read "no version prefix initially".)
4. Canonical machine-readable contract: `backend/api/openapi.yaml`, owned by the
   backend, plus a Go contract test that asserts every documented operation is
   registered in the router (coverage check) and every route has a spec entry.
   Hand-written spec + coverage test, no codegen requirement so far.
5. Frontend gets a small hand-written typed client (`frontend/src/api/`) over
   `fetch`; cookie-based session auth (ADR-0006) so the SPA keeps its current
   login UX. Unauthenticated responses are HTTP 401 with a stable error code;
   the SPA's login-redirect trigger replaces the legacy `Please login (10001)`
   string check (the legacy endpoint audit). If drift becomes a real problem,
   OpenAPI type generation can be added later without backend changes.
6. **CORS**: explicit allowlist from `CORS_ALLOWED_ORIGINS` (comma-separated
   exact origins, credentials allowed, never `*`). Preflight handled by a
   middleware in `internal/httpapi`. Because admin auth is cookie-based, all
   state-changing requests require a non-simple request (JSON content type +
   `X-CSRF: 1` header) so that cross-site forms cannot reach them; strict CORS
   allowlisting blocks the preflights.
7. The 28 legacy procedures map 1:1 to REST operations; the mapping table lives
   in `docs/API.md` and is the acceptance checklist.

## Consequences

- (+) Frontend is deployable independently (any static host / nginx), no Node at
  runtime for the backend, API is debuggable with curl.
- (+) Backend stays stdlib-only (`net/http` + ServeMux patterns) — no router or
  codegen dependency (ADR-0003).
- (−) Loses tRPC end-to-end type safety. Mitigated by the OpenAPI contract test
  and the 1:1 procedure→endpoint mapping checklist.
- (−) Cross-origin cookies force `SameSite=None; Secure` when frontend and
  backend are on different sites (consequence shared with ADR-0006/0007).
- Risk: MEDIUM — the frontend adaptation surface is the API mapping, not visuals;
  regression relies on the E2E checklist with the real SPA.

## Alternatives

- **tRPC wire compatibility** — no viable Go server implementation; would couple
  the backend to an unstable private protocol. Rejected.
- **gRPC-web** — needs an envoy/proxy layer and codegen; incompatible with the
  “one binary + existing frontend” constraint. Rejected.
- **GraphQL** — a large schema layer for 28 procedures and no client demand.
  Rejected (YAGNI).
- **Keep the Node backend** — out of scope; the rewrite is mandated.
