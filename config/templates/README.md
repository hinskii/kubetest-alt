# TestTemplate catalog

The ONLY layer where tool names exist. Every entry follows the CLAUDE.md
§11 catalog rules: pinned image tag verified at implementation time,
verdict strategy verified empirically (docker smoke passing + failing)
BEFORE commit, artifacts glob covers the tool's reports.

## Session A (commit 61139de)

| Template | Image | Verdict strategy | Notes |
|---|---|---|---|
| k6 | grafana/k6:1.4.0 | exit code | 99 = thresholds; loses script-error split (accepted) |
| cypress | cypress/included:15.5.0 | verdictFrom: junit | exit code caps at 255; template supplies /dev/shm 2Gi memory emptyDir |
| newman | postman/newman:6.1.3-alpine | exit code | JUnit reporter wired for count aggregation |
| jmeter | alpine/jmeter:5.6.3 | **verdictFrom: jtl, errorRateMax: "0"** | THE reason verdictFrom exists — JMeter's exit is always 0 |
| locust | locustio/locust:2.42.1 | exit code | Honest since ~2.15; plan's warning stale |
| playwright | mcr.microsoft.com/playwright:v1.56.0-noble | exit code + JUnit | reporter wired via env var |
| pytest | python:3.13-slim + pip install pytest==9.1.1 | exit code + --junit-xml | slow first-boot (pip); no widely-adopted pre-baked image |

## Session B

| Template | Image | Verdict strategy | Notes |
|---|---|---|---|
| gatling | **ghcr.io/hinskii/kubetest-alt/gatling:3.9.5** (platform-built) | exit code via `gatling-run` wrapper | ONLY platform-built tool image (see CLAUDE.md §3 exception); `gatling.sh` local mode returns exit=0 on failed assertion, our wrapper reads assertions.json and exits 2 |
| gradle | gradle:8.11-jdk21 | exit code | `gradle test` honest on JUnit failures |
| maven | maven:3.9-eclipse-temurin-21 | exit code | `mvn test` (surefire) honest on JUnit failures |
| artillery | artilleryio/artillery:2.0.34 | exit code, **REQUIRES `ensure` plugin in scenario** | legacy top-level `ensure` silently ignored in 2.x; template requires `ensure` config as marker |
| soapui | kubeshop/testkube-soapui-executor:2.1.123 (SoapUI 5.7.2) | exit code | testrunner.sh honest; template does NOT pass `-I` (would silence failures) |
| zap-baseline | ghcr.io/zaproxy/zaproxy:stable | exit code | 0=clean / 1=WARN / 2=FAIL / 3=err; `failOnWarn` config marker (default true = omit -I) |
| cucumber | ruby:3.3-slim + gem install cucumber 9.2.0 | exit code | cucumber-ruby honest; no standalone image → runtime install (same shape as pytest) |
| kubepug | kubeshop/testkube-kubepug-executor:2.1.123 | exit code, **REQUIRES `--error-on-deprecated`+`--error-on-deleted` flags** | without flags always exit 0; template always passes both |

## Docs-only (no template shipped)

Some tools are best consumed as raw Test manifests or need setup patterns
that don't fit a shared template. See:

- **curl** — trivial; `docs/examples/curl-raw-test.md` shows the pattern.
- **selenium** — needs webdriver services support (backlog);
  `docs/examples/selenium.md` sketches the target shape.
- **chainsaw** — kyverno testing framework; `docs/examples/chainsaw.md`
  documents current option (raw Test, no template until services land).
- **ginkgo** — deferred to a later session (backlog).

## SKIP

- **tracetest** — needs its own server (out of scope).
- **jmeterd** — distributed JMeter; requires ReadWriteMany PVC + services
  support; belongs after step 16 (helm) + services runtime.

## Onboarding a new tool

See [../../docs/onboarding-a-tool.md](../../docs/onboarding-a-tool.md).
