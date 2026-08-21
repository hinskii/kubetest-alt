# kubetest-alt web

SPA over the kubetest-alt apiserver. Step-18 scope: **read-only** views
(tests, runs, live logs, artifacts). Mutations land in step 19.

## Stack

- React 19 + TypeScript + Vite
- TanStack Query for server state
- react-router-dom v7 for routes
- Tailwind for tokens/utilities — theme reset to a six-color palette
  (see `tailwind.config.js`)
- API client GENERATED from `../openapi/openapi.json` via
  `openapi-typescript`, with a drift gate (`npm run check:client`)

## Local dev

Assumes a kind cluster running the operator + apiserver (see
`test/e2e/run.sh` in the repo root — it port-forwards the apiserver on
`127.0.0.1:18080`). Then:

```
cd web
npm install
npm run dev
# open http://localhost:5173
```

The dev server proxies `/api/*` to `KT_API` (default
`http://127.0.0.1:18080`). Set `KT_API` to point at a different
apiserver port-forward.

## Same-origin auth (production)

In production the GUI ships behind the same URL as the apiserver:
`/` serves the SPA, `/api/*` serves the REST/WS. That keeps cookies
issued by an SSO layer (oauth2-proxy / Istio / Ingress-managed OIDC)
usable without CORS. Step 20 documents the Helm values for that
layout; step 18 assumes the model.

## Scripts

| script                | what it does                                                 |
|-----------------------|--------------------------------------------------------------|
| `npm run dev`         | Vite dev server on :5173 with `/api` proxy                   |
| `npm run build`       | typecheck + production build to `dist/`                      |
| `npm run preview`     | serve `dist/` on :4173                                       |
| `npm run test`        | vitest + msw (jsdom)                                         |
| `npm run lint`        | eslint + `tsc --noEmit`                                      |
| `npm run gen:client`  | regenerate `src/api/generated.ts` from the OpenAPI spec      |
| `npm run check:client`| diff current generated client vs. spec — fails CI on drift   |
| `npm run screenshots` | Playwright captures for the step-18 report                   |

## Design decisions (short)

- **Mono-first typography.** Every text primitive is IBM Plex Mono.
  This is a tool for people who read logs; the UI IS the log. Not
  Inter + mono-for-code — Plex Mono throughout, weights 400/500/600.
- **Signature element = PhaseChip.** 4px color bar + uppercase mono
  phase word. Everywhere phases appear. The bar is the ONE affordance
  the eye tracks in a dense table; everything else is text on paper.
- **No cards, no shadows, no radius > 2px.** Full-bleed tables, 1px
  hairline dividers, band-striped rows via `bg-band` (barely there).
- **Colors earn their keep.** Six hex tokens named for meaning
  (`ink`, `bone`, `rule`, `pass`, `fail`, `err`, `run`, `pend`), no
  arbitrary Tailwind palette access.
- **Composite from day 1.** RunStepsTree understands the step-17
  key convention (`s{idx}` aggregates + `s{idx}/{test}[{i}]` children,
  `StepPhase="skipped"`); e2e scenario 6 is the reference fixture.
- **Explicit log states.** LogsViewer is a 5-state machine
  (connecting / replaying / live / closed-EOF / reconnecting). The
  post-terminal close is a DIFFERENT visual than a network glitch.

## Testing

- Vitest + testing-library + msw. Every view has loading / empty /
  error / happy tests.
- axe-core smoke on the four top-level routes (`src/test/a11y.test.tsx`).
- LogsViewer state machine tested with a hand-rolled WS mock — jsdom's
  own WebSocket is too coarse to script transitions.
- Playwright suite (`src/test/screenshots/`) shoots the report images
  against a real Chromium and the production build.

## Drift gate

The OpenAPI spec (`../openapi/openapi.json`) is the source of truth.
`src/api/generated.ts` is a checked-in derivative. CI runs
`npm run check:client` — a regenerate + diff. If the two differ, the
gate fails and points at `npm run gen:client`. Mirrors the Go-side
`openapi-check` on the same principle: the server and client cannot
silently drift.
