# TestTemplate catalog

The ONLY layer where tool names exist. Every entry follows the CLAUDE.md
§11 catalog rules: pinned image tag verified at implementation time,
verdict strategy verified empirically (docker smoke passing + failing)
BEFORE commit, artifacts glob covers the tool's reports.

## Session A (this commit)

| Template | Image | Verdict strategy | Notes |
|---|---|---|---|
| k6 | grafana/k6:1.4.0 | exit code | 99 = thresholds; loses script-error split (accepted) |
| cypress | cypress/included:15.5.0 | verdictFrom: junit | exit code caps at 255; template supplies /dev/shm 2Gi memory emptyDir |
| newman | postman/newman:6.1.3-alpine | exit code | JUnit reporter wired for count aggregation |
| jmeter | alpine/jmeter:5.6.3 | **verdictFrom: jtl, errorRateMax: "0"** | THE reason verdictFrom exists — JMeter's exit is always 0 |
| locust | locustio/locust:2.42.1 | exit code | Honest since ~2.15; plan's warning stale |
| playwright | mcr.microsoft.com/playwright:v1.56.0-noble | exit code + JUnit | reporter wired via env var |
| pytest | python:3.13-slim + pip install pytest==9.1.1 | exit code + --junit-xml | slow first-boot (pip); accepted, no pre-baked image is a community standard |

## Deferred to session B

- **gatling** — smoked with `denvazh/gatling:latest` (=v2.3.1, amd64
  emulated on arm64). Assertion violation reports exit=0 — image is too
  old to trust for a "verdict verified empirically" rule. Needs a
  modern Gatling image (community options thin; may end up bundling
  our own from openjdk + gatling zip). Deferred rather than shipping a
  template we can't back with a smoke.

## Onboarding a new tool

See [../../docs/onboarding-a-tool.md](../../docs/onboarding-a-tool.md).
