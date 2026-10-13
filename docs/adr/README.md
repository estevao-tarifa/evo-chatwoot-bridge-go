# Architecture Decision Records

Naming: `NNNN-<slug>.md`. Format: Context → Decision → Consequences →
Alternatives. Status: `Proposed` until accepted by the maintainer.

## Index

| ADR | Title | Status |
|---|---|---|
| [0001](0001-rest-instead-of-trpc.md) | REST/JSON API instead of tRPC | Accepted |
| [0002](0002-preserve-legacy-schema-migrations.md) | Preserve the legacy MySQL schema; new numbered forward-only migrations | Accepted |
| [0003](0003-go-dependency-budget.md) | Go dependency budget: stdlib first | Accepted |
| [0004](0004-persistent-idempotency.md) | Persistent idempotency via `processed_messages` | Accepted (+ Amendments 1–3) |
| [0005](0005-in-process-worker-queue.md) | In-process worker queue with bounded capacity | Accepted |
| [0006](0006-admin-auth-and-webhook-surface.md) | Mandatory admin authentication; restricted webhook surface | Accepted |
| [0007](0007-split-backend-frontend-deployments.md) | Split deployments: backend binary image, frontend nginx image | Accepted |
| [0008](0008-evolution-optional-capability-and-safe-errors.md) | Verified Evolution capabilities and opaque client errors | Accepted |
| [0009](0009-chatwoot-v4-identity-and-readback.md) | Exact Chatwoot identity and observed webhook state | Accepted |

Notes:

- Unverified facts are marked `[needs live validation]` (`AGENTS.md` §9).
- The canonical backend module map and the performance criteria live in
  `docs/ARCHITECTURE.md`.
