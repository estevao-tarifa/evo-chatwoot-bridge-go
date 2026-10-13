# ADR-0003 — Go dependency budget: stdlib first

- Status: Accepted
- Date: 2026-10-10
- Related: ADR-0002 (migration runner stays stdlib), ADR-0005 (worker queue
  stays stdlib)

## Context

`AGENTS.md` §4 fixes the dependency policy: prefer the standard library
(`net/http`, `encoding/json`, `context`, `database/sql`, `log/slog`, `sync`);
extra dependencies require a documented reason; the allowed baseline is
`go-sql-driver/mysql`, `golang.org/x/crypto/bcrypt` and
`prometheus/client_golang`. A bridge that forwards messages has a small,
well-understood problem space; every dependency is attack surface and a
maintenance commitment in an ops-critical binary.

Module path: `github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend`
(monorepo, one Go module under `backend/`). Toolchain: Go 1.27 (installed:
go1.27.2), `go 1.27` directive.

## Decision

**Direct dependencies are an allowlist, enforced in CI.**

| Dependency | Scope | Justification |
|---|---|---|
| `github.com/go-sql-driver/mysql` | runtime | only MySQL driver; `database/sql` needs one |
| `golang.org/x/crypto/bcrypt` | runtime | bcrypt per ADR-0006 (legacy uses `bcryptjs` cost 10) |
| `github.com/prometheus/client_golang` | runtime | metrics required by plan §7 |
| `github.com/stretchr/testify` | **test-only** | team convention (`AGENTS.md` §12); zero footprint in the shipped binary |

Anything else — runtime or test — requires an ADR update (or an explicit
justification block in the PR that adds it, accepted by the maintainer).
Enforcement: a CI step parses `go.mod` and fails on any direct require outside
the allowlist; `go mod verify` + `govulncheck ./...` run on every pipeline.

**Explicitly not dependencies — stdlib replacement:**

| Need | Used instead |
|---|---|
| HTTP router | `net/http.ServeMux` (Go 1.22+ method+wildcard patterns) |
| JSON | `encoding/json` |
| Logging | `log/slog` (JSON handler) |
| Config | `os.LookupEnv` + explicit validation in `internal/config` |
| Migrations | embedded SQL + ~150-line runner (ADR-0002) |
| Worker queue / retries | buffered channel + goroutines (ADR-0005) |
| Sessions / HMAC tokens | `crypto/hmac`, `crypto/rand` (`rand.Text()`), `encoding/base64` |
| Rate limiting | small in-memory token bucket (~40 lines, ADR-0006) |
| UUID-ish IDs / webhook tokens | `crypto/rand.Text()` |
| SMTP (notifications) | `net/smtp` (module is frozen but sufficient; revisit only if a real gap appears) |
| Timezone data in scratch image | `import _ "time/tzdata"` (stdlib) instead of shipping tzdata files |
| Assertions | stdlib `testing` + the testify allowlist above |

## Consequences

- (+) Minimal CVE surface, tiny binary (~scratch image), no framework lock-in,
  no codegen. `go.sum` committed; `govulncheck` in CI.
- (−) We hand-roll a small number of well-understood helpers (retry/backoff,
  migration runner, rate limiter, dotted-path parsers). Each must stay small and
  have tests; a helper growing beyond its slice is a signal to revisit this ADR.
- (−) No ORM: SQL is written and reviewed explicitly (also a feature — the
  queries are part of the performance contract, plan §7).
- Risk: LOW. Main failure mode is “reimplemented wheel” quality; mitigated by
  review +11 tests per helper and the contract that any helper stays < ~150 lines.

## Alternatives

- **“Popular stack” (chi/gin + viper + zap + golang-migrate + testify + …)** —
  rejected: nothing here needs a framework; violates the budget.
- **Only the MySQL driver** — rejected: bcrypt without `x/crypto` would mean
  hand-rolling crypto (never) and metrics are a roadmap requirement.
- **Vendoring** — not required (hermetic builds already guaranteed by `go.sum` +
  module proxy); revisit only if a target environment forbids the proxy.
