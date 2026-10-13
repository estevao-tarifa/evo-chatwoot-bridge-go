# Engineering skills — how this codebase is written

[README](../README.md) · [Architecture](ARCHITECTURE.md) · [Testing](TESTING.md) ·
[ADRs](adr/)

This repository is written and reviewed against a fixed set of Go engineering
skills. They are listed here because they are part of the contribution contract,
not decoration: a pull request is expected to follow them, and a review points at
the specific skill when it does not.

## 1. The skill pack

The pack is `samber/cc-skills-golang`, installed in the development workspace
under `.agents/skills/`. Skills are **not applied automatically** — they are
loaded explicitly before writing or reviewing code:

```text
skill://golang-how-to     ← the pack's index; load this first when unsure
```

`golang-how-to` routes to the rest of the pack. The load matrix below is the one
this project uses.

## 2. When to load what

| Task | Skills |
|---|---|
| Any Go code (style, naming) | `golang-code-style`, `golang-naming` |
| New packages / structure | `golang-project-layout`, `golang-design-patterns` |
| Writing tests | `golang-testing`, `golang-stretchr-testify` |
| Errors, wrapping, `slog` | `golang-error-handling` |
| Contexts, workers, races | `golang-context`, `golang-concurrency` |
| MySQL / storage layer | `golang-database` |
| Logs, metrics, tracing | `golang-observability`, `golang-samber-slog` |
| Security-sensitive code, review | `golang-security`, `golang-safety` |
| Lint / CI setup | `golang-lint`, `golang-continuous-integration` |
| Dependencies, library choice | `golang-dependency-management`, `golang-popular-libraries` |
| Benchmarks / performance work | `golang-benchmark`, `golang-performance` |
| Debugging a failure | `golang-troubleshooting` |

**Minimum bar per change:** whoever writes code loads the skills relevant to that
change *before* coding, and the hand-off report names the skills that were loaded.
The same applies to a reviewer. If a specialist is unavailable and a general
agent stands in, the rule still applies.

## 3. Where those skills are visible in this repository

Not claims — pointers you can check.

| Skill area | Evidence in the tree |
|---|---|
| **Dependency budget** | `docs/adr/0003-go-dependency-budget.md`: direct dependencies are an allowlist, enforced in CI (`.github/workflows/backend.yml`). Today: `go-sql-driver/mysql` + `golang.org/x/crypto` only. `prometheus/client_golang` is reserved for the metrics phase |
| **Error handling** | ~150 `fmt.Errorf(… %w …)` sites and ~180 `errors.Is`/`errors.As` checks; sentinel errors per package; no `panic` on the request path |
| **Contexts** | `context.Context` is threaded from ingress to every upstream call; cancellation is honoured on drain |
| **Concurrency** | `internal/worker` is a bounded, `sync`-only pool (`ADR-0005`); the test suite runs under `-race` |
| **Data structures / DB** | `database/sql` with explicit transactions, parameterised queries only, and a forward-only migration runner (`ADR-0002`) with checksum locking and `GET_LOCK` |
| **Observability** | `log/slog` structured logging everywhere; `internal/observability` holds the payload sanitiser that strips tokens and message bodies before anything is persisted |
| **Security** | `ADR-0006`: session + CSRF on admin routes, per-connection HMAC webhook tokens, bcrypt users, masked secrets, fail-closed ingress |
| **Testing** | 51 test files, 200+ table-driven subtests, stdlib-only (`testing`); integration tests run against real MySQL 8 **and** 9; mutation checks are used to prove a test can fail |
| **CI** | `.github/workflows/backend.yml` — build, `go vet`, race tests, `govulncheck`, plus the dependency-allowlist gate |

Two deliberate deviations:

- **`golang-stretchr-testify` is permitted, but the suite does not use it.**
  ADR-0003 allows it as a test-only dependency; the current tests are written with
  `testing` alone. Consistency beats adopting a helper midway — if you add
  testify, convert a whole package, not one file.
- **No mocks for the upstreams.** The Evolution and Chatwoot clients are exercised
  against fixtures and recorded payload shapes, and the storage layer against a
  real database, because the failure modes that matter here (identity reuse,
  duplicate delivery) are interactions, not units.

## 4. Skills and AI agents

If you use an AI coding agent on this repository, give it the same contract:

1. Tell it to load the skills from §2 **before** it edits code.
2. Require it to name the skills it loaded in its report.
3. Keep QA and code review by an agent **different** from the implementer.
4. Require the exact test command and its observed result, not a claim that tests
   pass.
5. Never let it print or persist a token, cookie, media body or message content.

`AGENTS.md` in the development workspace holds the full workflow
(`SPEC → ARCHITECTURE → CONTRACT → IMPLEMENTATION → TEST → REVIEW → DOCS →
COMMIT`); the parts that bind external contributors are in
[CONTRIBUTING.md](../CONTRIBUTING.md).
