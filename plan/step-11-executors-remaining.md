# Step 11 — Remaining executors (cypress/newman/locust/jmeter)

## Goal
Wrapper images + Runners + parsers for the other four tools (CLAUDE.md §11 table, §15.2 exit codes).

## Tasks
- newman: `-r cli,json,junit`; verdict from JSON `run.stats.assertions`.
- cypress: junit reporter; verdict from JUnit (exit code only as fallback, capped-255 aware §15.2); compiler already mounts `/dev/shm`.
- locust: `--headless --csv=out`; verdict from CSV failure ratio vs configurable threshold (`config: failureRatioMax`), exit code untrusted (§15.2).
- jmeter: `-l out.jtl -e -o report`; **verdict computed from JTL** (assertion failures / error rate vs `config: errorRateMax`), exit code 0 NEVER trusted (§15.2); HTML report scraped as artifact.
- Each: Dockerfile in `executors/<tool>/`, Runner registered, parser in scraper registry (step 07).

## Unit test requirements
- Per tool, golden fixture suite in `testdata/` from REAL runs (commit actual outputs): passing run, failing run, empty/truncated output, malformed file. Parser → exact expected ExecutionResult (counts, phase, metrics).
- JMeter regression suite (mandatory, §15.2): JTL with 100% errors + exit 0 → phase `failed`; JTL all-success + exit 1 → phase `error` (tool broke); error rate exactly at threshold → boundary behavior documented + tested both sides.
- Locust: failure ratio 0 with exit 0 → passed; ratio > threshold → failed; missing CSV → error.
- Cypress: JUnit says 3 failed + exit 3 → failed with counts; 260 failures + exit 255 → counts from JUnit not exit code.
- newman: assertion failures → failed; collection parse error → error.
- Exit-code normalization tables extended per tool in `pkg/executor` (same pattern as k6 step 05).

## Acceptance
- kind e2e per tool: sample test runs end-to-end → correct phase + artifacts + parsed metrics in status.

## NOTE (from step 05)
Dispatch design: **one `/entry` binary shared by all five wrapper images**, Runner selected by `ExecutionRequest.Type` from a package-level registry (`map[string]executor.Runner`). Per-tool `/entry` binaries were considered and rejected — five entrypoints to maintain for no dispatch-cost benefit (Type-based lookup is O(1)).
- Step 05 shipped: `ExecutionRequest.Type` field (compiler sets it from `Test.spec.type`); k6 `Runner.Validate` rejects mismatched Type with a clear message.
- Step 11 wires: `cmd/entry/main.go` builds `map[string]Runner{"k6": k6.NewRunner(), "cypress": cypress.NewRunner(), ...}` and dispatches by `req.Type`. Extend `executor.Entry` to accept the map instead of a single Runner (already a small refactor — the current single-Runner field becomes `Runners map[string]Runner`).
- Each wrapper `Dockerfile` copies the same `/entry` binary. Only the base image differs (grafana/k6, cypress/included, postman/newman, locustio/locust, alpine/jmeter).
- Golden files already carry `command: [/entry]` on all five executor containers (step 05); no compiler change needed here.
