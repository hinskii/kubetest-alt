# Step 21 — Faster CI ✅

Every push waits ~10–13 minutes for CI, and most of that time is spent
setting up, not testing. A Catalog job runs its tool for 17 s and spends
about 7.5 minutes getting there. This step cuts the waiting without dropping
a single check. It is done now, between 20R's R2 and R3, because every later
step pays for slow CI on every push.

**Goals**
- PR feedback (every required workflow green) in about 5 minutes wall clock.
- Same coverage: every test, gate and scenario that runs today still runs.
  The e2e still tests the real Dockerfiles.
- Measured before and after, per job (table in §4).

**Non-goals:** dropping tests or matrix legs, self-hosted runners, paid
larger runners.

---

## 1. Where the time goes today (measured, 2026-10-11)

| Workflow | Job | Total | Of which |
|---|---|---|---|
| Tests | Run on Ubuntu | 5m 53s | `go test -race` 4m 20s, of it ~2m 45s compiling before the first result; vet 28s; setup-go 28s |
| Tests | Integration | 4m 56s | ~2m race compile; then **every** package's tests, unit tests included (they already ran in the job above) |
| Tests | Control Center UI | 46s | — |
| Lint | golangci-lint | 1m 59s | not broken down yet |
| E2E | s3 / gcs | 10m 52s / 12m 42s | **4 image builds 6m 24s**; kind 40s; Playwright browser + deps install; Go scenarios 2m 44s (cron waits for a minute boundary); Playwright 10s |
| Catalog | 15 jobs, one per tool | 8m 26s – 12m 20s | k6 for example: free disk space 2m; kind 45s; **image builds 4m 49s**; the tool itself 17s |

Each of the 17 kind jobs (2 E2E + 15 Catalog) builds the same five
platform images from scratch:
- every `docker build` runs `go mod download`;
- `go build -a` disables Go's build cache;
- the builds run one after another;
- nothing is cached between runs.

## 2. Changes (one commit each, gates green, measured)

### F1 — Build the platform images once per workflow run

- **Shared build job.** A reusable workflow `.github/workflows/build-images.yml` (`workflow_call`) builds the platform images:
  - operator, apiserver, control-center (with the UI), content-fetcher, and the oauth2-proxy re-tag;
  - built with `docker/build-push-action` and the BuildKit cache in GitHub Actions (`cache-from/to: type=gha`, one scope per image);
  - `docker save` writes them out, zstd compresses them, and they are uploaded as one artifact.
- **Callers.** E2E and Catalog each call it once (`needs: images`). Every kind job downloads the artifact and runs `docker load`.
- **`test/e2e/run.sh`** gets `E2E_PREBUILT_IMAGES=1`. With it the script skips the builds. It still checks that every image is present, as it does today, so a missing image fails loudly. Locally nothing changes: without the variable it builds as before.
- **Dockerfiles** (`Dockerfile`, `executors/content-fetcher/Dockerfile`):
  - drop `-a`;
  - add BuildKit cache mounts for the module cache and the build cache (`RUN --mount=type=cache,target=/go/pkg/mod` and `/root/.cache/go-build`);
  - the web stage gets a cache mount for pnpm's store.

  These also help `make dev-reload` locally.

Expected: E2E −5 min per job, Catalog −4.5 min per job. On a warm cache the
build job is ~1–1.5 min and runs once per workflow, not 17 times.
Risk: the GitHub Actions cache is capped at 10 GB per repository; the
images are small (distroless/alpine), so scopes per image fit. The artifact
is ~200 MB compressed and downloads in seconds.

### F2 — Integration runs only the integration tests

The Integration job adds `-tags integration` but runs the whole module, so the
unit tests run a second time, with `-race`. The integration code lives in
`internal/store` and `pkg/storage`, the packages its coverage gate already
names (`COVERAGE_PKGS_INTEGRATION`). It will run only the packages that
contain integration-tagged files, found with `go list` filtered by build tag
so that a new integration test is picked up without editing the Makefile.

Expected: −2.5 to −3 min. Unit coverage is unchanged; it comes from the Tests
job.

### F3 — Go build cache per job

`setup-go` caches under one key per `go.sum`. Whichever job saves first
wins, so the race-instrumented builds of the Tests job are usually not in
the cache it restores. Each job will cache its own `GOCACHE` with
`actions/cache` (key: job + OS + Go version + `go.sum` hash, restore keys
for partial hits). The module cache stays shared.

Expected: Tests −2 min on a warm cache. The race compile no longer starts
from zero.

### F4 — E2E scenarios in parallel

The ten Go scenarios run one after another. The cron scenario alone waits
up to a minute for the next minute boundary, and the others mostly wait on
pods. They will run with `t.Parallel()` in their own namespaces or with
names that don't collide. The checks that read operator-wide state (the
`kubetest_runs_total` metric, the operator's error-free log) move to a final
step that runs after all scenarios.

Expected: Go scenarios from 2m 44s to ~1m 15s (the cron minute).
Risk: shared-state assumptions between scenarios. Each scenario gets
reviewed for that, and the cluster is small, so parallelism is bounded by
the scenario count.

### F5 — Cache Playwright's browser

`~/.cache/ms-playwright` cached on the Playwright version from
`pnpm-lock.yaml`. On a hit only the system dependencies install
(`playwright install-deps chromium`).

Expected: −30 to −60 s per E2E job.

### F6 — Catalog's disk cleanup

"Free disk space" deletes several SDKs with `rm -rf` and takes 2 min. Once
F1 lands, the job no longer builds images, and the space it needs is the
tool's image plus the platform's. Check `df` with and without the cleanup on
the biggest tools (cypress, zap, playwright):
- if it is still needed, delete only the largest directory, or delete in the background while kind starts;
- if not, drop the step.

Expected: up to −2 min per Catalog job.

### F7 — Measure, then decide: lint and the controller's envtest

- **Lint:** golangci-lint takes ~2 min. Check whether its cache is restored (`~/.cache/golangci-lint`) and whether it runs only once.
- **Controller tests:** `internal/controller` takes 66 s in envtest. Look for fixed sleeps and slow `Eventually` polling intervals.

These stay in the plan only if the measurement shows a real win.

### F8 — Keep BuildKit's cache mounts between runs (added after R3) — measured, reverted

**Problem.** After F1, an image build was fast only when no Go code changed. A Go change invalidated the build layer, and the Dockerfiles' cache mounts start empty on every runner, so the first Go image compiled every dependency again: ~2 min, and the images job took ~5 min instead of ~40 s.

**Change.** The mounts are kept in GitHub Actions' cache and injected into the builder with `reproducible-containers/buildkit-cache-dance`:
- the mounts kept are `gomod`, `gobuild` and `pnpm-store`;
- the key covers `go.sum`, `**/*.go`, the pnpm lockfile and the Dockerfiles;
- on a hit, nothing was compiled, so saving is skipped;
- on a change, the newest cache is restored, only what changed is compiled, and the result is saved.

**First attempt.** The key covered only the dependencies. A run that compiled nothing then saved the still-empty mounts (0 MB), and every later run would have hit that key. Adding the Go sources to the key fixed it.

**Measurement (R4a, R4b: two consecutive commits that change Go code).** It made the job slower.

| images job, Go changed | Before F8 | With F8 |
|---|---|---|
| R4a (no saved mounts yet) | ~5 min | 7m 8s (E2E), 4m 50s (Catalog) |
| R4b (mounts saved by R4a) | ~5 min | 8m 49s (E2E), 8m 24s (Catalog) |

With R4a's mounts restored, the operator still took ~1 min (2m 13s cold), so compiling got about a minute faster. The cache cost more than that:
- restoring the mounts and injecting them into the builder: ~1m 15s;
- after the builds, extracting them from the builder: ~3m 15s;
- saving them: ~30 s.

The mounts are large, and the key changes on every Go commit, so every such commit pays both ways. Reverted: the workflow is back to the layer cache (F1), which already makes a run that changes no Go code fast.

## 3. Order

1. F2 (smallest change, clear win) → F3 → F1 (biggest, touches both kind
   workflows and the Dockerfiles) → F5 → F6 → F4 → F7.
2. After each commit, compare the job durations with §1 and write them in §4.
3. The E2E failure on the s3 leg of R2 (the fixture run never reached
   passed) gets its own fix first: it is a correctness problem, not speed.

## 4. Results ✅

Measured on GitHub Actions, warm caches. Wall clock is push to the
workflow's result.

| Workflow / job | Before | After | Change |
|---|---|---|---|
| Tests / Run on Ubuntu | 5m 53s | 2m 13s – 2m 17s | −3m 40s (F3) |
| Tests / Integration | 4m 56s | 21s – 28s | −4m 30s (F2) |
| Lint | 1m 59s | 57s – 1m 4s | −1m (F3) |
| Build images (once per workflow) | — | 39s – 44s | new (F1) |
| E2E job (s3 / gcs) | 10m 52s / 12m 42s | 4m 13s / 4m 36s | about −7m (F1, F3, F4) |
| E2E Go scenarios | 2m 44s | 70s | −1m 34s (F4) |
| **E2E wall clock** | **~12–13 min** | **5m 5s – 5m 19s** | about −7m |
| Catalog job (per tool) | 8m 26s – 12m 20s | 2m 23s – 3m 26s | about −7m each (F1, F6) |
| **Catalog wall clock** | **~12–13 min** | **4m 10s** | about −8m |
| Controller envtest (local, -race) | ~65s | ~44s | −21s (F7) |

Notes:
- **Cold caches** (the first run after `go.sum`, a Dockerfile or the
  Makefile changes): the image build takes 5–6 min and the Tests job ~6 min.
  Even then the run is no slower than before.
- **F5 dropped:** the browser download is a few seconds; caching it isn't
  worth a cache entry.
- **Go's test cache:** with the build cache kept (F3), unchanged packages
  report `(cached)`. Go re-runs a package when its code, its dependencies or
  the files and environment variables it reads change. The integration
  tests (containers) and the kind e2e always run (`-count=1` in run.sh).
- **Runner minutes per push:** Catalog ~150 → ~45 min; E2E ~23 → ~10 min.

## 5. Acceptance

- Every job and check that runs today still runs and is green.
- Targets:

  | Job | Target |
  |---|---|
  | E2E | ≤ 5 min |
  | Catalog | ≤ 4 min plus the tool's own time |
  | Tests | ≤ 3 min |
  | Integration | ≤ 2 min |

  All on a warm cache. A cold cache may be slower, but never slower than today.
- `make dev-reload` and `test/e2e/run.sh` still work locally without any new
  variables.
