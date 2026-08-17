# Step 02 — CRD types + webhooks

## Goal
`api/v1alpha1`: Test, TestRun, TestTemplate, TestTrigger per CLAUDE.md §10 sketches. Validation + defaulting webhooks.

## Tasks
- Types exactly as §10 (incl. `Phase` with `error`, `TestRunStatus.resolvedSpec`, generic `PodConfig` passthrough — §8: NO hardcoded annotations anywhere).
- Printcolumns, status subresources, enum markers.
- Validating webhook (Test): executor type enum; inline `content.files[].content` total size <= 512KB (§15.7); cron `schedule` parses (robfig/cron); `concurrencyPolicy` enum; git content requires `uri`.
- Validating webhook (TestRun): `testRef` non-empty; config override keys must exist in referenced Test `spec.config` — SKIP cross-object lookup in webhook (race-prone), do it in controller; webhook validates shape only.
- Defaulting: `concurrencyPolicy=Allow`, `source=api` when unset.

## Unit test requirements
- Table-driven validation tests (no cluster): each rule above — valid case + >=2 invalid cases with exact error substring asserted.
  - inline content 511KB passes, 513KB fails with message containing "use git/tarball".
  - cron: `"0 6 * * *"` ok, `"61 * * * *"` fails, empty ok (manual).
  - PodConfig: annotations map passes through validation untouched — assert NO key is rejected or mutated (regression guard for §8 no-hardcoding).
- Defaulting tests: unset → defaults set; explicit values never overwritten.
- envtest: CRDs install, create valid Test + TestRun succeeds, invalid rejected by webhook with 400.
- Golden test: generated CRD YAML in `config/crd` matches committed files (zero-diff gate already covers; add explicit test loading YAML and asserting enum values for Phase).

## Acceptance
- `kubectl apply` of sample manifests in `config/samples/` (one per CRD) succeeds on envtest.
