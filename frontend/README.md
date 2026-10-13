# frontend — Bridge WhatsApp admin SPA

React 19 + Vite + Tailwind 4 + shadcn/ui, adapted from the legacy client to talk
to the Go bridge over **REST/JSON** (no tRPC, no superjson, no backend module
import). It builds and deploys independently: the backend never serves its assets
(ADR-0007).

## Commands

```sh
npm ci
npm run dev        # Vite dev server (proxies /api to VITE_API_PROXY_TARGET, default http://localhost:8080)
npm run typecheck  # tsc --noEmit
npm test           # vitest (structural + client tests)
npm run build      # static build into dist/
```

## Configuration

The API base URL is read at runtime from `window.__BRIDGE_CONFIG__.apiBaseUrl`
(`public`-served `config.js`). The container entrypoint rewrites it from the
`BRIDGE_API_BASE_URL` env var. Empty (the default) means same-origin `/api/v1`,
which is the recommended reverse-proxy setup (ADR-0007 §5).

## Deployment

`Dockerfile` builds the assets with Node and serves them with `nginx:alpine`
(SPA fallback + gzip). Only static files ship at runtime.

## Contract

`src/api/` is the typed REST client. `src/api/routes.ts` is the single source of
truth for paths; `tests/routes-contract.test.ts` checks it against
`backend/api/openapi.yaml`.

> The two `routes-contract` OpenAPI assertions activate only once
> `backend/api/openapi.yaml` is on `main`. Until then a green `npm test` is not
> OpenAPI-verified.

Secret handling (ADR-0006 Am. 4): credentials are **write-only** — never fetched
or displayed; the UI shows set/unset and a blank field keeps the stored value.
The token-bearing webhook URL is revealed only on explicit user action and is not
persisted.
