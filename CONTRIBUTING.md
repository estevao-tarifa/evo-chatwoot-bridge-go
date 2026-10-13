# Contributing

Thanks for considering a contribution. This project is a **rewrite of a working
production bridge**, so the bar is higher than "it compiles": behaviour is
preserved only when a test proves it, and every change is reviewed against the
specification rather than against the implementation.

Read this file, then [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md),
[`docs/TESTING.md`](docs/TESTING.md) and the ADRs in
[`docs/adr/`](docs/adr/) before you open a PR.

## 1. The mandatory workflow

```
SPEC → ARCHITECTURE → CONTRACT → IMPLEMENTATION → TEST → REVIEW → DOCS → COMMIT
```

No stage may be skipped:

1. **SPEC** — the change is written down (a short spec or an issue the
   maintainer accepted). *Nothing* starts before a spec exists.
2. **ARCHITECTURE** — if the change alters a boundary, a dependency direction or
   a data flow, it needs an ADR in `docs/adr/` (copy the numbering scheme and
   `Status:` header from the existing ones). Accepted ADRs are authoritative;
   do not silently contradict one.
3. **CONTRACT** — external API/field assumptions live in `docs/contracts/`
   (Evolution GO, Chatwoot) and in `backend/api/openapi.yaml` for our own REST
   surface. **Never invent an upstream endpoint, field or capability**; if it
   cannot be verified from the upstream source or a live instance, mark it
   `[needs live validation]` and say so in the PR.
4. **IMPLEMENTATION** — smallest change that satisfies the spec. Reuse existing
   packages and patterns; do not introduce a second convention for something the
   repo already solves. New third-party dependencies require an ADR: the budget
   is small by design (standard library first, then the documented allowlist).
5. **TEST** — see §4. Tests assert the specification, never "what the code
   currently does".
6. **REVIEW** — every change gets an **independent QA pass and an independent
   review pass**, in sessions that did not write the code. The reviewer runs the
   tests, tries to falsify the claims, and records severity
   (CRITICAL/HIGH/MEDIUM/LOW) with a minimal reproduction. A change does not
   advance on the author's word alone.
7. **DOCS** — update every document the change invalidates: `docs/API.md` when a
   route or payload changes, the ADR when a decision changes, `deploy/README.md`
   when an operator step changes, `CHANGELOG.md` under `## [Unreleased]`.
8. **COMMIT** — one small, focused commit (see §2).

A change is *done* only when: implementation + tests **executed and reported**
(exact command and its result) + docs updated + OpenAPI consistent (if an
endpoint changed) + a clean working tree + one Conventional Commit.

## 2. Commit messages — Conventional Commits

Format:

```
<type>(<scope>): <imperative summary>

<body: what changed and why, not how>

<trailers, e.g. Refs: #123, Closes: #456>
```

Allowed types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`,
`ci`, `chore`, `merge`.

Scopes in use: `backend`, `frontend`, `bridge`, `config`, `httpapi`, `admin`,
`auth`, `api`, `dedup`, `storage`, `evolution`, `chatwoot`, `cli`, `deploy`,
`adr`, `specs`, `reports`, `docs`, `ci`, `frontend`.

Rules:

- Subject in the imperative mood, lower case, no trailing period, ≤ 72 chars.
- One logical change per commit. Do not mix a refactor with a behaviour change
  — the reviewer has to be able to diff intent.
- The body explains **why** and cites the spec/ADR/finding id.
- Never mention a real hostname, token, phone number or customer name.

## 3. Branches and pull requests

- Branch from `main`, name it after the change (`feat/dedup-quarantine`).
- Keep `main` green: no PR merges red CI.
- A PR description contains: what and why, the files that changed, **the exact
  test command and its output**, the ADR/spec it implements, and the review
  verdict. Template: summary · architectural decisions · files · tests executed ·
  problems and fixes · audit result · remaining risks.
- Two things reviewers always check: does the change weaken an existing
  regression test, and does it log a secret. Either is a blocker.

## 4. Tests must be run and reported

CI runs, from `backend/`:

```bash
gofmt -l .            # must print nothing
go vet ./...
go build ./...
go test -race -shuffle=on ./...
go mod verify
```

and from `frontend/`:

```bash
npm ci
npm run typecheck
npm test
npm run build
```

Storage-layer tests need a real MySQL (`TEST_MYSQL_DSN`, MySQL 8 **and** 9 in
separate `*_test` schemas, serial with `-p 1`); they are skipped without it, and
the CI integration job fails on any skip. Copy the exact command you ran into
the PR — "tests pass" without the command is not evidence, and a claim that could
not be verified in your environment must be labelled `[needs live validation]`.

Going further: `docs/TESTING.md` lists the mandatory regression matrix
(identity reuse, dedup, loop-break, retry/quarantine, media types, CORS, OpenAPI
coverage). Add a case there before fixing a bug, so the bug cannot come back
unnoticed.

## 5. Go skills (mandatory before writing or reviewing Go)

The project is developed with the `samber/cc-skills-golang` skill pack. Skills
are **not** applied automatically: load the relevant ones **before** writing or
reviewing Go code, and **name them** in your hand-off/PR description (e.g.
"loaded: golang-code-style, golang-testing"). If you are unsure which apply, load
`golang-how-to` first. Minimum bar per change: whoever touches the engine, the
persistence layer or the observability layer, plus the QA pass, states the skills
they loaded. This applies to AI agents and humans alike.

Typical mapping:

| Touching | Load |
|---|---|
| Any Go code | `golang-code-style`, `golang-naming` |
| New packages/layout | `golang-project-layout`, `golang-design-patterns` |
| Tests | `golang-testing`, `golang-stretchr-testify` |
| Errors, slog | `golang-error-handling` |
| Contexts, workers, races | `golang-context`, `golang-concurrency` |
| MySQL / storage | `golang-database` |
| Logs, metrics, tracing | `golang-observability`, `golang-samber-slog` |
| Security-sensitive code, review | `golang-security`, `golang-safety` |
| Lint / CI | `golang-lint`, `golang-continuous-integration` |
| Dependencies | `golang-dependency-management`, `golang-popular-libraries` |
| Benchmarks / performance | `golang-benchmark`, `golang-performance` |
| Debugging a failure | `golang-troubleshooting` |

## 6. Migrations are forward-only

The database schema is shared with the historical (Node) bridge, so migrations
are **additive, forward-only and idempotent** (ADR-0002):

- New SQL file in `backend/migrations/`, `NNNN_snake_case.sql`, embedded and
  applied in filename order. Never edit or renumber a migration that has
  already been released — its checksum/filename is the record that it ran.
- `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX` style only. No `DROP`, no
  `DELETE`, no `UPDATE` of existing data, no column rewrite, and never touch the
  legacy migration bookkeeping table.
- The runner takes an advisory lock (`GET_LOCK('evo_bridge_migrate')`), so two
  concurrent `bridge migrate` runs cannot race.
- Migration runs **separately from the server** (the binary does not migrate at
  boot): `bridge migrate` is a one-shot step executed *before* the new version
  starts. A rollback of the code must not require reverting a migration.
- Anything destructive is a manual, operator-approved procedure, never a
  migration file.

## 7. Non-negotiables

- **Never** log tokens, auth headers, cookies, media base64, full payloads or
  private message content. The sanitizer in `internal/observability` is the only
  place that decides what reaches a log line.
- **Never** print a real secret, phone number or customer name — not in a test,
  a fixture, a commit, a PR or a screenshot. Synthetic data only.
- Admin routes require an authenticated session; there is no public fallback.
- Identity integrity invariants (external messages never create duplicate
  contacts/conversations; a group's identity is its JID, never its display name)
  are regressions that must fail-then-pass. See `docs/ARCHITECTURE.md` §4.
- Do not weaken an existing test to make a change pass. If a test is wrong, say
  so in the PR with the reasoning and the spec it contradicts.
- One bridge per connection: never run two bridges against the same Evolution
  instance or Chatwoot inbox.

## 8. Reporting bugs and vulnerabilities

Bugs: open an issue with the deployment shape, the exact reproduction and the
sanitized log lines (see the redaction rules in §7).

Security problems: **never** in a public issue — follow
[SECURITY.md](SECURITY.md).
