# Step 20R — Control Center in React

Control Center (CC) today is Go html/template pages plus small same-origin
scripts (≈ 6.2k lines of Go in `internal/controlcenter/server`, 13 page
templates, 4 scripts). Interaction-heavy parts — the wizard with a live YAML
panel, per-tool source choices, a namespace that reloads the catalog,
drop-downs, the planned drawer and Settings tab — are DOM patching that gets
harder with every feature (hidden-attribute bugs, re-binding after swaps,
two code paths with and without JavaScript). CLAUDE.md §13 planned a SPA in
`web/` from the start; this step does it, **before the rest of step 20**:
20a–20g and the remaining 20j parts are then built in React.

**Goals**
- The whole CC UI in React + TypeScript, built to static files embedded in
  the CC binary (`go:embed`) — one image, one deploy, as now.
- The Go CC stays: sign-in (oauth2-proxy headers), roles, clusters, the
  service-proxy calls to each cluster's kubetest API, artifact and
  live-view serving, metrics — and becomes the SPA's backend-for-frontend
  (BFF): a JSON API under `/api/v1`.
- Stateless as before ([[feedback-control-center-stateless]]): no database.
- Feature parity first, then the step-20 improvements; no feature lost.

**Non-goals:** a different backend, server-side rendering (Next.js), a
second deployment, talking to the kubetest API from the browser directly
(tokens and cluster credentials stay server-side).

---

## 1. Stack (decided, mature libraries only — no hand-rolled widgets)

| Concern | Choice | Why |
|---|---|---|
| Language / build | TypeScript (strict), **Vite** | standard, fast, static output |
| UI framework | **React 19** | ecosystem, team familiarity |
| Components | **shadcn/ui** (Radix primitives + Tailwind CSS) | accessible primitives (select, combobox, dialog/drawer, tabs, tooltip), components copied into the repo — no runtime lock-in |
| Styling | **Tailwind CSS 4** with CC's design tokens (light/dark) | tokens as CSS variables, `data-theme` switch kept |
| Routing | **TanStack Router** (file-based, typed params/search) | typed URLs for `/clusters/$cluster/…`, search params (filters, `?range=`) |
| Server state | **TanStack Query** | caching, polling (logs, live runs, lists every 10 s), invalidation after mutations |
| Forms | **React Hook Form** + **Zod** | the wizard, Settings sections, run form |
| Tables | **TanStack Table** | tests list, runs, test cases |
| Charts | **Recharts** | analytics trends, duration bars, pass-rate |
| Code / YAML | **CodeMirror 6** via `@uiw/react-codemirror` (+ lang-yaml, -javascript, -python, -xml) | editing and read-only views, one look; lazy-loaded |
| Icons / toasts | lucide-react, sonner | shadcn defaults |
| API types | **openapi-typescript** + openapi-fetch from the BFF's OpenAPI | types generated, never hand-written |
| Tests | **Vitest** + Testing Library + **MSW** (mocked BFF); **Playwright** e2e | unit/component + browser flows |
| Lint/format | ESLint (typescript-eslint, react-hooks, jsx-a11y), Prettier | |
| Package manager | **pnpm** through **corepack** (`packageManager: pnpm@12.11.2`), Node pinned (`.nvmrc` 24) | reproducible, lockfile committed; pnpm's release-age guard on (`pnpm-workspace.yaml`) |

Versions are pinned exactly (`pnpm add -E`). TypeScript is 6.0.x:
typescript-eslint 8.71 supports `<6.1`.

Chroma (server-side highlighting) and Tom Select (vendored) go away at the
end (R5): CodeMirror read-only and shadcn's combobox replace them.

## 2. Architecture

```
browser ── oauth2-proxy ── CC (Go)
                            ├─ /                      SPA: index.html + /assets/* (embedded, hashed)
                            ├─ /api/v1/…              BFF JSON (this step)
                            ├─ /clusters/…/artifacts/… artifacts, sandboxed (as now)
                            ├─ /live/{token}/…        live view proxy (as now)
                            └─ /healthz /readyz /metrics (ops listener, as now)
                                   │ service proxy, X-Kubetest-Token
                                   ▼
                            kubetest API server(s) per cluster
```

- **Serving:** `GET /assets/*` from the embedded `web/dist` (immutable,
  hashed names); any other GET that isn't an API/artifact/live path
  returns `index.html` (SPA routes keep today's URLs:
  `/clusters/{c}/tests/{ns}/{name}`, `/clusters/{c}/runs/{ns}/{id}`, …, so
  links and bookmarks keep working).
- **BFF (`/api/v1`)** — JSON, the user's identity from the sign-in header,
  role checks as today (`auth.Require`), cross-origin protection unchanged
  (fetch from the same origin passes `CrossOriginProtection`). Mostly thin
  wrappers over `pkg/apiclient`; CC-side computation stays in Go
  (analytics summaries, run comparison, schedules overview, cleanup
  preview, the editor's build/preview/dry run, YAML formatting).
- **OpenAPI for the BFF**, reflected from the handlers' Go types
  (swaggest/openapi-go) through one route table that also registers the
  handlers — a route can't exist without its spec entry. Written by
  `cmd/gen-openapi`, zero-diff checked in CI; the TS client is generated
  from it (`pnpm gen:api`), also zero-diff checked.
- **CSP: scripts stay `'self'`; the UI document allows inline styles.**
  Vite emits no inline scripts (the theme bootstrap is the shared
  `/static/theme.js`). sonner and Radix inject `<style>` elements at run
  time, so `index.html` is served with `style-src 'self' 'unsafe-inline'`
  (`webui.CSP`, found in R0's browser check). Style injection can restyle a
  page but not run code; the server-rendered pages keep the stricter policy;
  artifacts and live views keep their sandbox CSPs. Playwright fails any
  test that logs a console error, so a CSP violation can't slip in (R1
  caught Zod's eval probe this way: Zod runs `jitless`).
- **Auth/roles in the SPA:** `GET /api/v1/me` → email, role; the UI hides
  what a role can't do, the BFF enforces it.

### BFF endpoints (parity with today's pages and actions)

| Area | Endpoints |
|---|---|
| Session | `GET /me` |
| Clusters | `GET /clusters` (with reachability) |
| Tests | `GET /clusters/{c}/tests` (list + status), `GET …/tests/{ns}/{name}` (resolved, conditions, params), `DELETE …` (admin) |
| Runs | `GET …/tests/{ns}/{name}/runs?after=&phase=`, `POST …/tests/{ns}/{name}/run`, `GET /clusters/{c}/runs/{ns}/{id}` (run + events + children + failed cases), `GET …/log?offset=` (text, as now), `POST …/abort`, `…/rerun`, `PUT/DELETE …/comment`, `DELETE …` (admin) |
| Analytics | `GET …/tests/{ns}/{name}/analytics?runs=`, `GET …/runs/{ns}/{id}/compare?with=`, `…/compare.md` (download) |
| Test cases | `GET …/tests/{ns}/{name}/cases?runs=&sort=`, `GET …/cases/history?case=` |
| Schedules | `GET /clusters/{c}/schedules`, `PUT …/tests/{ns}/{name}/schedule` |
| Cleanup | `GET …/cleanup` (preview), `POST …/cleanup` (admin) |
| Editor | `GET /clusters/{c}/namespaces` (Tests ∪ catalogs), `GET …/templates?namespace=`, `POST …/tests/preview` (build → YAML + problems, the live panel), `POST …/tests?dryRun=` (create / check), `PATCH …/tests/{ns}/{name}?dryRun=` (edit, conditional on resourceVersion), `GET …/tests/{ns}/{name}/manifest` (YAML for Git) |
| Live view | `GET …/runs/{ns}/{id}/live` → signed `/live/{token}/` URL (as today) |

Kept server-side as non-API routes: artifacts (sandbox CSP + storage shim),
`logs.txt` download, live view proxy.

## 3. Repository layout

```
web/                      # the SPA (pnpm workspace root for the UI)
  package.json, pnpm-lock.yaml, .nvmrc, vite.config.ts, tsconfig.json
  src/
    routes/               # TanStack Router file routes = today's URLs
    components/ui/        # shadcn/ui components (copied, owned)
    components/           # CC components: YamlView, CodeEditor, PhaseBadge, RunLog, …
    api/                  # generated client + TanStack Query hooks
    lib/                  # theme, formatting (durations, dates), roles
  e2e/                    # Playwright specs
internal/controlcenter/
  api/                    # the BFF handlers (new)
  webui/                  # go:embed of web/dist + SPA fallback handler (new)
  server/, views/         # today's pages — removed route by route, gone in R5
```

## 4. Build, CI, image, dev loop

- **Makefile:** `web-install`, `web-dev` (Vite dev server, proxies `/api`,
  `/live`, `/clusters/*/runs/*/artifacts` to the dev cluster's CC on
  :8090), `web-build`, `web-test`, `web-lint`, `web-typecheck`,
  `web-gen-api` (+ `-check`), `web-size`, `web-audit`, `web-licenses`,
  `web-e2e`. `make build` does not need Node: a binary built without the UI
  serves a "run make web-build" page; images build it (`WITH_WEB=true`).
- **Docker:** `--build-arg WITH_WEB=true` (Control Center only) selects a
  `node` stage that builds the UI into `internal/controlcenter/webui/dist`, copied into the Go builder
  before `go build` (go:embed); final image unchanged (distroless).
  `.dockerignore` re-includes `web/` sources (the dockerignore test learns
  the new embed).
- **CI:** a `web` job — pnpm install (cached), typecheck, lint, Vitest,
  build, generated-client zero-diff, bundle-size budget, `pnpm audit`
  (high/critical fail), license allowlist (MIT/Apache-2.0/BSD/ISC). The e2e
  job adds Playwright against the kind cluster (CC behind oauth2-proxy as
  today) for the main flows.
- **Dev:** `make dev-up` builds the image with the SPA; for UI work
  `make dev-ui` + `make web-dev` gives hot reload against the dev cluster.

## 5. Migration phases (each a commit series with green gates)

Strangler pattern: the Go server serves the SPA for routes already moved,
the old pages for the rest; a route moves when its React page reaches
parity (and its old template and handler are deleted in the same commit).

| Phase | Scope | Exit criteria |
|---|---|---|
| **R0 Foundation** ✅ | `web/` scaffold, toolchain, CI job, Docker stage, embed + SPA fallback, theme (tokens, light/dark, the toggle), app shell (top bar, user + role, breadcrumbs, notices/toasts, error boundary, 404), BFF skeleton (`/me`, `/clusters`), OpenAPI + TS generation, MSW + Playwright harness | the home page in React in the image on kind; e2e sign-in path unchanged. Done: unknown addresses get the UI with a 404 (its not-found page); breadcrumbs come with R1's first nested page |
| **R1 Browse** ✅ | tests list (groups, filters, locked/not-ready chips, auto-refresh), Test page (header, params, run form incl. "start at", schedule panel, run history paging, definition in CodeMirror read-only, delete/duplicate/edit links) | parity checklist ✓; old templates `home`, `tests`, `test` deleted. Done: BFF `listTests`, `getTest`, `deleteTest`, `setSchedule`, `listTestRuns`, `startRun`; the server's helpers it shares moved to `api/view.go`; Go page tests and BFF tests share `cctest` (real kubetest API on fake k8s); tables use shadcn's Table — TanStack Table comes with the first sortable table (R3 test cases) |
| **R2 Runs** ✅ | run page: facts, Now: (events), live log (polling via Query), log download, live view iframe, report iframe + dark inversion + original colours, failed cases, metrics, child runs, comment, abort / rerun / delete, Kubernetes events panel, Run again | parity ✓; `run.html`, `run.js` deleted. Done: BFF `getRun` (one view model: facts, steps, children, artifacts + gallery, events, failed cases, signed live link — the UI keeps the first link so the frame doesn't reload), `getRunLog` (text + X-Run-Phase/X-Next-Offset/X-More), `abortRun`, `rerun`, `setComment`/`deleteComment`, `deleteRun`; the run page polls every 5 s until the run finishes, the log every 2 s; the schedules page keeps the Go abort route until R3; `cctest` gained the run-history and test-case fakes |
| **R3 Insights** ✅ | analytics (trends, summary, comparison + markdown download), test cases + history, schedules overview, cleanup preview/apply | parity ✓; remaining read pages deleted. Done in four commits (schedules, cleanup, test cases + history + run compare, analytics); every computation moved to the API package with its tests (summaries, trends, comparison + Markdown, case diff, cleanup criteria). Tables that sort use TanStack Table v9; sparklines are Recharts (code-split with the analytics page, budgeted separately); Markdown downloads in the browser (no compare.md route). Left server-side: the editor (R4), layout and error templates (R5) |
| **R4 Editor (= 20j in React)** ✅ | the 3-step wizard (what → where → parameters), tool and namespace comboboxes (namespace typed or picked, catalog follows), per-tool source rules (projects git-only, inline single-file, none), inline script editor (CodeMirror by file type), test data rows, live YAML panel (BFF preview, debounced), dry run on Create, Edit, Duplicate, YAML mode (CodeMirror + server line errors), the Test page's **Settings** tab with per-section saves | the 20j acceptance (3 screens for an inline k6 Test, git Playwright) in Playwright; `editor.*`, `refresh.js` deleted In three commits. **R4a ✅** the editor's build and its JSON API (namespaces, catalog, preview, create, form, update). **R4b ✅** the wizard in React (`/tests/new`, Duplicate via `?from=`): live YAML from the API's preview (always 200; nothing to build = no YAML, the page keeps the last), CodeMirror for the script and YAML mode with the server's errors at their lines, the Go new-test page deleted, Playwright acceptance (inline k6 in three screens, Playwright from git, YAML line markers). CodeMirror's core is a named chunk shared with the read-only view; the editor's languages have their own budget (46 kB), the all-JS budget went 450 → 500 kB. **R4c ✅** the Test page's Settings tab: General, Source, Test data, Parameters, Container, Resources, Pod, Schedule, Timeout and YAML, each saved alone (the form as the server sent it plus that section's draft, conditional on its resourceVersion; a 409 offers a reload; untouched sections follow a newer version, drafts stay); Git-managed Tests and viewers read-only, with Duplicate; YAML-only Tests show the YAML section. The Go editor (`editor.html`, `editor.js`, `refresh.js`, its handlers and tests) and Tom Select are deleted; `/edit` redirects to `?tab=settings`. Playwright: a section saves on the e2e cluster |
| **R5 Clean-up** ✅ | remove `views/` templates, static scripts, Tom Select, Chroma highlighting endpoint/CSS, the HTML-scraping e2e in `test/e2e/controlcenter_test.go` → BFF + Playwright; docs (control-center.md, getting-started, security: CSP unchanged, BFF auth), CLAUDE.md §13 layout | no Go template left; coverage gates moved (`internal/controlcenter/api` ≥ 85%, web ≥ 80% lines) Done: `views/` (layout and error templates, app.css), Chroma (`yamlview` keeps only the YAML formatting the API uses) and `/static/` are gone; the pre-paint theme script moved into the web build (`/theme.js`, revalidated); errors on the routes left in Go (log and artifact downloads, the live view) are plain text. The e2e scenario only GETs and calls the JSON API. Coverage: `internal/controlcenter/api` stays at its 90% gate; web fails under 80% of lines (Vitest, v8; 88% now). Docs: control-center (wizard, Settings), getting-started, security (the page's CSP, the API behind the same sign-in, cross-origin protection), CLAUDE.md §13 |

Step 20 then continues in React: 20a drawer (shadcn Sheet + TanStack
Router search param, ↑/↓ between runs), 20b–20c stats (Recharts), 20d log
viewer (CodeMirror read-only with search), 20e CLI panel, 20f labels, 20g
about.

## 6. Parity checklist (must exist before a page's template is deleted)

- Every action and role gate of today (developer: run, run at a time,
  run again, abort, comment, schedule, create/edit/duplicate; admin:
  delete runs/tests, cleanup).
- GitOps lock rules (read-only Tests, Duplicate instead of Edit) and the
  not-ready conditions.
- Auto-refresh of lists (10 s, paused in hidden tabs), live log polling,
  page refresh on phase change → TanStack Query refetch/invalidation.
- Notices after actions (toasts), confirmation for destructive actions
  (shadcn AlertDialog instead of `<details>`).
- Dark/light theme incl. the tool frames' inversion and "Original colors".
- Links stay the same URLs; deep links into runs/tests work; 404s render
  in the shell.
- Downloads (logs.txt, YAML, compare.md, artifacts) and sandboxed frames.

## 7. Testing

- **Go:** BFF handler tests against the real kubetest API on fake k8s
  (as `pages_test.go` does today, but JSON assertions); OpenAPI zero-diff.
- **Web:** Vitest + Testing Library + MSW per page/component (forms,
  role gates, error states); typecheck is a gate.
- **Browser:** Playwright on kind in CI — sign-in path, create an inline
  k6 Test with the wizard and run it, see live log and report, abort a
  run, edit with a conflict, dark theme screenshot diff of key pages; axe
  accessibility checks on each page.
- The headless-Chrome/CDP scripts used during 20j become Playwright
  specs.

## 8. Risks and how they're handled

- **Big-bang regressions** → strangler per route, parity checklist,
  old page deleted only when its React page passes e2e.
- **Supply chain** (npm) → lockfile, pinned Node/pnpm, `pnpm audit` and a
  license allowlist in CI, few deps (shadcn components are copied code).
- **Bundle size** → route-level code splitting, CodeMirror and Recharts
  lazy; budget in CI (initial JS ≤ 250 KB gzip).
- **Two UIs during migration** → one shell look (same tokens), both read
  the same BFF/apiclient; the period is bounded by this step.
- **No-JS fallback is dropped** (accepted: CC is an internal tool behind
  sign-in); the server keeps no HTML pages after R5.

## 9. Acceptance (whole step)

- Every CC route served by the SPA; Go templates and page handlers gone.
- CI: web job green (typecheck, lint, Vitest, build, audit, licenses,
  bundle budget); Go gates green; Playwright e2e green on kind.
- `make dev-up` + `make web-dev` hot-reload loop documented and working.
- Docs updated; CLAUDE.md §13 says `web/` is the UI, CC the BFF.
