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
| gatling | **ghcr.io/hinskii/kubetest-alt/gatling:3.9.5** (platform-built) | exit code via `gatling-run` wrapper | Platform image (see CLAUDE.md §3 exception); `gatling.sh` local mode returns exit=0 on failed assertion, our wrapper reads assertions.json and exits 2 |
| gradle | gradle:8.11-jdk21 | exit code | `gradle test` honest on JUnit failures |
| maven | maven:3.9-eclipse-temurin-21 | exit code | `mvn test` (surefire) honest on JUnit failures |
| artillery | artilleryio/artillery:2.0.34 | exit code, **REQUIRES `ensure` plugin in scenario** | legacy top-level `ensure` silently ignored in 2.x; template requires `ensure` config as marker |
| soapui | **ghcr.io/hinskii/kubetest-alt/soapui:5.7.2** (platform-built) | exit code | Platform image; bundles SoapUI OSS 5.7.2 (last public release) — mini-15B closing swapped away from kubeshop/testkube-soapui-executor |
| zap-baseline | ghcr.io/zaproxy/zaproxy:stable | exit code | 0=clean / 1=WARN / 2=FAIL / 3=err; `failOnWarn` config marker (default true = omit -I) |
| cucumber | ruby:3.3-slim + gem install cucumber 9.2.0 | exit code | cucumber-ruby honest; no standalone image → runtime install (same shape as pytest) |
| kubepug | **ghcr.io/hinskii/kubetest-alt/kubepug:1.7.1** (platform-built) | exit code, **REQUIRES `--error-on-deprecated`+`--error-on-deleted` flags** | Platform image (alpine + Go binary); mini-15B closing swapped away from kubeshop/testkube-kubepug-executor |

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

## HTML reports (step 18g)

Templates name their main report in `spec.artifacts.report`; Control
Center shows it as "Open report" on the run page.

| Template | Report | How it is produced |
|---|---|---|
| jmeter | `repo/results/report/index.html` | `-e -o` dashboard from the JTL |
| gatling | `repo/results/**/index.html` | Gatling's own report (whole results folder collected) |
| k6 | `repo/results/k6-report.html` | built-in web dashboard, `K6_WEB_DASHBOARD_EXPORT`; k6 writes it only after two aggregation periods — `config.dashboardPeriod` (default 10s, so runs under ~20s get none; short tests set e.g. `1s`) |
| locust | `repo/results/locust-report.html` | `--html` |
| playwright | `results/playwright-report/index.html` | `--reporter=junit,html`, `PLAYWRIGHT_HTML_OPEN=never` |
| zap-baseline | `repo/results/zap-report.html` | `-r` |
| gradle | `**/build/reports/tests/test/index.html` | Gradle's test report |

No report: artillery (HTML reports were removed from Artillery 2.x),
cypress, newman, pytest, maven, cucumber, soapui, kubepug — they would
need reporter plugins the vendor images don't ship. Functional tools show
their test cases, screenshots and videos instead (step 18-2f).
