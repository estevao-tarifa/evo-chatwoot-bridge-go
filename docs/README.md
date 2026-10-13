# Documentation index

Start here: [Install](INSTALL.md) · [Configure](CONFIGURATION.md) ·
[Troubleshoot](TROUBLESHOOTING.md) — and the [README](../README.md)
([PT-BR](../README.pt-BR.md)).

## Using it

| Document | What it answers |
|---|---|
| [INSTALL.md](INSTALL.md) / [INSTALL.pt-BR.md](INSTALL.pt-BR.md) | How to deploy with Docker Compose or EasyPanel, create the first admin and connection, wire both webhooks, verify end to end, upgrade and roll back |
| [CONFIGURATION.md](CONFIGURATION.md) | Every environment variable and every per-connection field, webhook token derivation, SPA runtime config, migration semantics, tuning knobs |
| [TROUBLESHOOTING.md](TROUBLESHOOTING.md) | Symptom → cause → fix for the failure modes seen in real deployments (401/503 ingress, silent delivery stops, quarantine, SPA 404/CORS, group identity) |
| [ARCHITECTURE.md](ARCHITECTURE.md) | System overview, module map and dependency rules, key flows, identity invariants, data ownership, security invariants, risks |
| [API.md](API.md) | The admin REST surface: conventions, route map, error catalogue, secret handling, deliberate gaps, persistence |
| [TESTING.md](TESTING.md) | Test strategy: what is unit vs integration, how the suites are run, what the CI enforces |
| [SKILLS.md](SKILLS.md) | The Go engineering practices this codebase is written to, and where each is visible in the tree |

## Reference

| Document | Contents |
|---|---|
| [contracts/chatwoot/](contracts/chatwoot/) | Chatwoot v4.12.1 contract: contacts, conversations, inboxes, messages, webhooks, allowed operations, hypotheses |
| [contracts/evolution/](contracts/evolution/) | Evolution GO contract: auth, send API, webhooks, JID/LID/groups, response formats, hypotheses |
| [bridge/ROUTING_MATRIX.md](bridge/ROUTING_MATRIX.md) | The canonical routing decision matrix (events E1–E14, preconditions) |
| [bridge/OPEN_CONFLICTS.md](bridge/OPEN_CONFLICTS.md) | Conflicts between the two upstream contracts and how each was resolved |
| [adr/](adr/README.md) | Architecture Decision Records — REST over tRPC, legacy schema preservation, dependency budget, idempotency, worker queue, auth surface, split deployments, Evolution error semantics, Chatwoot identity/read-back |

The contracts and the routing matrix are the specification the tests assert
against; a behaviour that contradicts them is a bug in the code, not a doc bug.

## Conventions

- `[needs live validation]` marks a behaviour that was established from the
  upstream source and official documentation but not re-confirmed against a live
  instance. Treat those as unverified until you have checked them yourself.
- The deployment, configuration and tuning numbers in these documents come from
  a real single-host deployment; where a value is a starting point rather than a
  measurement, it is described as one.
