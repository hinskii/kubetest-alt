# Step 10 — API server for GUI

## Goal
`cmd/apiserver` + handlers: thin REST/WS layer over CRs + store (CLAUDE.md §7). GUI consumes only this.

## Tasks
- Endpoints: `GET/POST /tests`, `GET /tests/{name}`, `PATCH /tests/{name}`, `POST /runs`, `GET /runs` (cluster actives + store archive merge), `GET /runs/{id}`, `GET /runs/{id}/logs` (WS), `GET /runs/{id}/artifacts/*` (presigned MinIO URLs).
- Reads via shared informer cache (controller-runtime cluster client), NOT raw watches (§15.4).
- **managed-by enforcement (§7)**: `PATCH`/`DELETE` on `managed-by: gitops` Test → 409 with message "managed by GitOps — edit in repo"; `POST /runs` referencing a gitops Test → ALLOWED. GUI-created Tests get `managed-by: ui`, `source: ui` on runs.
- Log WS bridges logstream (step 08); archive runs stream from MinIO object instead.

## Unit test requirements (httptest, fake client + fake store — no cluster)
- managed-by table (core §7 regression suite): {gitops+PATCH→409}, {gitops+DELETE→409}, {gitops+POST run→201}, {ui+PATCH→200}, {missing label treated as ui→200}.
- POST /tests: sets managed-by=ui exactly once; user-supplied managed-by=gitops in payload rejected 400 (spoof guard).
- Run listing merge: active (cluster) + archived (store) — no duplicates for a run present in both (UID dedupe), ordering by startedAt desc, pagination cursor stable.
- WS logs: fake stream → client receives replayed buffer + live lines; disconnect mid-stream doesn't panic server (goroutine leak check with goleak).
- Artifact URLs: presigner behind interface; expiry set; path traversal in artifact name rejected.
- Error mapping: NotFound→404, webhook validation error→400 with message passthrough, conflict→409.

## Acceptance
- goleak clean on full handler suite; OpenAPI spec generated and committed (zero-diff gate).
