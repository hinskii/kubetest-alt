# Step 15 (REPLACES step-15-generic-executor) — Tool template catalog (Testkube parity)

> Depends on step 13 (templates machinery) and the step-11 workflows refactor.
> Execution order: 11(refactor) → 12 → 13 → 15 → 14. 2 sessions: core / extended.

## Goal
Curated TestTemplate catalog in `config/templates/` — the ONLY layer where tool names exist.
Every template: pinned image, command, artifacts glob, `kubetest.io/tool` label, verdict
mitigation IN the template where the tool's exit code lies. The former first-class 5 join the
catalog as ordinary entries.

## Catalog rules (every template)
- Verdict source verified empirically (docker smoke passing + failing) BEFORE commit.
- Pinned image tag verified at impl time; versions in report.
- Artifacts glob covers the tool's report output; JUnit-writing tools point reports into the
  artifacts dir so scraper counts flow.
- Sample Test per template in config/samples/tools/; samples apply-test covers all.
- docs/onboarding-a-tool.md: honest exit code + JUnit → plain template (~15 min); lying exit
  code → template + verdictFrom (or a tool flag); metrics wanted → scraper perf parser.

## Session A — core catalog
| Template | Verdict strategy | Notes |
|---|---|---|
| k6 | exit code (0 pass, non-zero fail — 99 thresholds included) | loses failed-vs-error split for script bugs (107→failed); documented, acceptable |
| cypress | exit code (= #failed) + JUnit counts | template supplies /dev/shm memory emptyDir via pod.volumes (moved from compiler in step 11) |
| newman | exit code + JUnit reporter | honest non-zero on assertion failures |
| jmeter | **verdictFrom: jtl, errorRateMax: "0"** | THE reason verdictFrom exists; exit 0 never trusted |
| locust | verify empirically at impl: headless exit-code behavior; if it lies → template flags or verdictFrom extension | CSV → scraper metrics |
| playwright | exit code + JUnit | |
| pytest | exit code + `--junit-xml` | |
| gatling | exit code (non-zero on failed assertions) | HTML report artifact |

## Session B — extended
| Template | Verdict notes |
|---|---|
| gradle / maven | exit code honest; JUnit glob (build/test-results, surefire) |
| curl | exit code; no reports |
| ginkgo | exit code + --junit-report |
| artillery | REQUIRES `ensure` config param in template (without it: always exit 0) |
| soapui | junit output flag; verify exit-code-on-failure at impl |
| zap-baseline | exit 0/1/2; template config failOnWarn maps 1 |
| cucumber | junit formatter; exit honest |
| kubepug | --error-on-deprecations |
| selenium (pytest+webdriver) | ADVANCED, example-only — needs services support (backlog) |
| chainsaw | optional; verify junit at impl |

SKIP: tracetest (own server), distributed jmeterd.

## Acceptance
- kind e2e: k6, jmeter (failing plan → failed via jtl), cypress, playwright samples end-to-end;
  `kubectl get tests` Tool column shows names from labels.
- Docker smokes passing+failing per tool with versions.
- Report per session: mapping, versions, gates + commit + push + git log.
