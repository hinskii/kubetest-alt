# Run metrics

Every run can carry two kinds of numbers:

- **Test counts** (`status.testCounts`: total / passed / failed / skipped)
  from any JUnit XML the run scrapes as an artifact. No configuration.
- **Load metrics** (`status.metrics`) from the tool's own report, when the
  Test (usually via its catalog template) declares `spec.metrics`.

Both are recorded on the TestRun, in run history and in webhook payloads.
Neither ever changes the run's verdict — that is `spec.verdict`'s job.

## Declaring a report

```yaml
spec:
  metrics:
    from: k6Summary
    path: "repo/results/summary.json"
```

`path` is a doublestar glob relative to the wrapper's working directory
(`/data` unless `container.workingDir` is set — tools write under
`/data/repo/results/`). When several files match, the lexicographically
last one wins, so timestamped result directories resolve to the newest
run. A missing or unparseable report leaves `status.metrics` empty and is
logged by the wrapper (`metricsError` in `result.json`).

| `from`          | Tool      | File                                        | Catalog template |
|-----------------|-----------|---------------------------------------------|------------------|
| `k6Summary`     | k6        | `k6 run --summary-export <file>`            | `k6`             |
| `jtl`           | JMeter    | `jmeter -n -l <file>.jtl` (CSV)             | `jmeter`         |
| `locustCsv`     | Locust    | `locust --csv <prefix>` → `<prefix>_stats.csv` | `locust`      |
| `gatlingStats`  | Gatling   | `<results>/<simulation>-<epoch>/js/stats.json` | `gatling`     |
| `artilleryJson` | Artillery | `artillery run --output <file>.json`        | `artillery`      |

Functional tools (Cypress, Playwright, pytest, Maven, Gradle, Newman,
Cucumber, SoapUI) report through JUnit → test counts. Security/lint tools
(ZAP, kubepug) report through their verdict and artifacts.

## Vocabulary

Consumers (Control Center, dashboards) render by key, never by tool. Keys
a tool doesn't report are absent — never zero.

| Key               | Meaning                                    | k6 | JMeter | Locust | Gatling | Artillery |
|-------------------|--------------------------------------------|:--:|:------:|:------:|:-------:|:---------:|
| `requests`        | requests sent                              | ✓ | ✓ | ✓ | ✓ | ✓ |
| `errors`          | failed requests (see below)                | ✓ | ✓ | ✓ | ✓ | ✓ |
| `error_rate`      | `errors / requests`, 0–1                   | ✓ | ✓ | ✓ | ✓ | ✓ |
| `rps`             | throughput, requests/second                | ✓ | ✓¹ | ✓ | ✓ | ✓ |
| `latency_avg_ms`  | mean response time                         | ✓ | ✓ | ✓ | ✓ | ✓ |
| `latency_min_ms`  |                                            | ✓ | ✓ | ✓ | ✓ | ✓ |
| `latency_med_ms`  | median                                     | ✓ | ✓¹ | ✓ | ✓ | ✓ |
| `latency_p90_ms`  |                                            | ✓ | ✓¹ | ✓ |   | ✓ |
| `latency_p95_ms`  |                                            | ✓ | ✓¹ | ✓ | ✓² | ✓ |
| `latency_p99_ms`  |                                            | ✓³ | ✓¹ | ✓ | ✓² | ✓ |
| `latency_max_ms`  |                                            | ✓ | ✓ | ✓ | ✓ | ✓ |
| `checks_passed`   | k6 checks that passed                      | ✓ |   |   |   |   |
| `checks_failed`   | k6 checks that failed                      | ✓ |   |   |   |   |
| `iterations`      | k6 iterations                              | ✓ |   |   |   |   |
| `data_received_bytes` |                                        | ✓ |   |   |   |   |
| `data_sent_bytes` |                                            | ✓ |   |   |   |   |
| `vus_max`         | peak virtual users                         | ✓ |   |   |   |   |

Runs judged by `verdict.from: junit` also carry `tests_total`,
`tests_passed`, `tests_failed` and `tests_skipped` in `status.metrics`
(the same numbers as `status.testCounts`, there for charting), and
`verdict.from: jtl` adds `requests`, `errors`, `error_rate` from the
verdict's own pass over the JTL.

**Error** means no response or an HTTP status ≥ 400, i.e. what the tool
itself counts as a failure — except Artillery, whose `errors.*` counters
only cover transport failures; for it, `errors` adds every `http.codes.4xx`
and `http.codes.5xx`.

1. JMeter only writes per-sample rows; throughput is
   `samples / (last end − first start)` and percentiles use the
   nearest-rank method over every sample.
2. Gatling's `percentiles1..4` follow `gatling.conf`
   (`charting.indicators.percentile1..4`, defaults 50/75/95/99); the
   mapping assumes the defaults.
3. Only when the script sets `summaryTrendStats` to include `p(99)`.

## Fixtures

Parsers are tested against real tool output under `pkg/report/testdata/`,
produced by `hack/report-fixtures.sh` with the catalog's images against a
target that answers `/` with 200 and `/missing` with 404 (so every fixture
has ~50% errors). Re-run it after bumping a tool image.
