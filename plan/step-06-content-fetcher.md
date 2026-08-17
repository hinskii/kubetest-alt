# Step 06 — Content fetcher

## Goal
Init-container mode of `cmd/entry` (`/entry fetch`): populate `/data` from Content spec (git/files/tarball) per CLAUDE.md §12.

## Tasks
- git: clone `uri@revision`, sparse checkout `paths[]`, auth from mounted secrets/env (basic/header/ssh). Shallow by default.
- files: write inline/`contentFrom` files with `mode`.
- tarball: download + unpack to `path`.
- All failures → exit non-zero with machine-readable last line (`FETCH_ERROR: <reason>`) that the controller maps to phase `error`, reason `ContentFetchFailed` (§15.7). Hard timeout on fetch.

## Unit test requirements
- git against local fixture repos (`git init` in t.TempDir(), commits created in test): happy clone; nonexistent revision → FETCH_ERROR; sparse path missing → FETCH_ERROR mentioning the path; auth failure (bad token against local http git server httptest) → FETCH_ERROR without leaking token in message (assert token absent from output).
- tarball: valid tar.gz unpacks; **zip-slip/path traversal**: entry `../../etc/evil` MUST be rejected (security test, mandatory); symlink escaping /data rejected.
- files: mode applied (0755 executable), nested dirs created, contentFrom resolution behind interface with fake.
- Timeout: fetch exceeding limit killed with FETCH_ERROR: timeout.

## Acceptance
- Controller (step 04) surfaces ContentFetchFailed with human-readable message in TestRun status (envtest e2e: init container fails → phase error).

## NOTE (from step 03)
Content spec currently rides in `KUBETEST_CONTENT_JSON` env var on the init container — fine for git/tarball (small JSON), breaks for inline files (webhook admits up to 512KB aggregate). Env-embedded 512KB bloats the pod object in etcd (~1.5MB pod limit), leaks into `kubectl describe`, and risks hitting `ARG_MAX`/exec limits on large values. Move content delivery into the existing request ConfigMap in this step (add a `content.json` key alongside `request.json`, mount both under `$KUBETEST_REQUEST_DIR`), and drop `KUBETEST_CONTENT_JSON` from the init container env. Compiler-side change is small (one extra CM key, one extra mount, one env var removal). Add a test with aggregate inline content near the 512KB limit flowing end-to-end (compile → apply → init container reads content from mounted file).
