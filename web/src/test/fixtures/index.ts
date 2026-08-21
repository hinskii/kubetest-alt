// Fixtures modeled on real cluster payloads. The composite fixtures
// mirror scenario 6 in test/e2e/e2e_test.go (smoke → load, skip-on-
// fail) so the GUI is exercised with the same shape the operator
// produces in the kind e2e.

import type { RunEnvelope } from '../../api/client'

export const k6Test = {
  metadata: {
    name: 'k6-smoke',
    namespace: 'kubetest-e2e',
    labels: {
      'kubetest.io/tool': 'k6',
      'app.kubernetes.io/managed-by': 'ui',
    },
  },
  spec: {
    concurrencyPolicy: 'Allow',
    schedule: '',
    container: {
      image: 'grafana/k6:1.4.0',
      command: ['k6'],
      args: ['run', '/data/repo/script.js'],
    },
  },
  status: {
    latestRun: {
      name: 'k6-smoke-abc',
      phase: 'passed',
      finishedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
    },
  },
}

export const jmeterTest = {
  metadata: {
    name: 'jmeter-load',
    namespace: 'kubetest-e2e',
    labels: {
      'kubetest.io/tool': 'jmeter',
      'app.kubernetes.io/managed-by': 'gitops',
    },
  },
  spec: {
    concurrencyPolicy: 'Forbid',
    use: ['jmeter'],
    verdict: { from: 'jtl', errorRateMax: '0' },
    config: {
      plan: { type: 'string', default: 'smoke.jmx' },
    },
  },
  status: {
    latestRun: {
      name: 'jmeter-load-def',
      phase: 'failed',
      finishedAt: new Date(Date.now() - 12 * 60_000).toISOString(),
    },
  },
}

export const compositeTest = {
  metadata: {
    name: 'suite-nightly',
    namespace: 'kubetest-e2e',
    labels: {
      'kubetest.io/tool': 'composite',
      'app.kubernetes.io/managed-by': 'gitops',
    },
  },
  spec: {
    concurrencyPolicy: 'Allow',
    steps: [
      {
        name: 'smoke',
        execute: { tests: [{ name: 'k6-smoke' }] },
      },
      {
        name: 'load',
        condition: 'passed',
        execute: { tests: [{ name: 'jmeter-load' }] },
      },
      {
        name: 'cleanup',
        condition: 'always',
        optional: true,
        execute: { tests: [{ name: 'k6-smoke' }] },
      },
    ],
  },
  status: {
    latestRun: {
      name: 'suite-nightly-xyz',
      phase: 'failed',
      finishedAt: new Date(Date.now() - 30 * 60_000).toISOString(),
    },
  },
}

export const testsFixture = [k6Test, jmeterTest, compositeTest]

// Runs envelope shape (see internal/apiserver/handlers_runs.go).
export const runsFixture: RunEnvelope[] = [
  {
    uid: 'r-1',
    name: 'k6-smoke-abc',
    namespace: 'kubetest-e2e',
    testRef: 'k6-smoke',
    phase: 'passed',
    source: 'cron',
    origin: 'cluster',
    queuedAt: new Date(Date.now() - 5 * 60_000).toISOString(),
    startedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
    finishedAt: new Date(Date.now() - 4 * 60_000 + 8_000).toISOString(),
    durationMs: 8_040,
  },
  {
    uid: 'r-2',
    name: 'jmeter-load-def',
    namespace: 'kubetest-e2e',
    testRef: 'jmeter-load',
    phase: 'failed',
    source: 'api',
    origin: 'archive',
    queuedAt: new Date(Date.now() - 15 * 60_000).toISOString(),
    startedAt: new Date(Date.now() - 14 * 60_000).toISOString(),
    finishedAt: new Date(Date.now() - 14 * 60_000 + 14_000).toISOString(),
    durationMs: 14_035,
    message: 'verdictFrom=jtl: error rate 1.0000 exceeds 0.0000',
  },
  {
    uid: 'r-3',
    name: 'suite-nightly-xyz',
    namespace: 'kubetest-e2e',
    testRef: 'suite-nightly',
    phase: 'failed',
    source: 'trigger',
    origin: 'cluster',
    queuedAt: new Date(Date.now() - 30 * 60_000).toISOString(),
    startedAt: new Date(Date.now() - 29 * 60_000).toISOString(),
    finishedAt: new Date(Date.now() - 29 * 60_000 + 22_000).toISOString(),
    durationMs: 22_400,
    message: 'composite: 1 passed, 1 failed, 1 skipped',
  },
]

export const compositeRun = {
  metadata: {
    name: 'suite-nightly-xyz',
    uid: 'r-3',
    namespace: 'kubetest-e2e',
    labels: { 'kubetest.io/tool': 'composite' },
  },
  spec: { testRef: 'suite-nightly', source: 'trigger' },
  status: {
    phase: 'failed',
    message: 'composite: 1 passed, 1 failed, 1 skipped',
    queuedAt: runsFixture[2]!.queuedAt,
    startedAt: runsFixture[2]!.startedAt,
    finishedAt: runsFixture[2]!.finishedAt,
    durationMs: runsFixture[2]!.durationMs,
    steps: {
      s0: { phase: 'passed', startedAt: runsFixture[2]!.startedAt, finishedAt: runsFixture[2]!.startedAt, durationMs: 8_000 },
      's0/k6-smoke[0]': { phase: 'passed', startedAt: runsFixture[2]!.startedAt, finishedAt: runsFixture[2]!.startedAt, durationMs: 7_500 },
      s1: { phase: 'failed', startedAt: runsFixture[2]!.startedAt, finishedAt: runsFixture[2]!.finishedAt, durationMs: 14_000 },
      's1/jmeter-load[0]': { phase: 'failed', startedAt: runsFixture[2]!.startedAt, finishedAt: runsFixture[2]!.finishedAt, durationMs: 13_800 },
      s2: { phase: 'skipped', startedAt: runsFixture[2]!.finishedAt, finishedAt: runsFixture[2]!.finishedAt, durationMs: 0 },
    },
    counts: { total: 2, passed: 1, failed: 1, skipped: 0 },
    metrics: { samples_total: 1, samples_failed: 1, error_rate: 1 },
    artifactRefs: [
      { key: 'results/jmeter.jtl', bucket: 'kubetest-artifacts', sizeBytes: 3421 },
      { key: 'results/jmeter.log', bucket: 'kubetest-artifacts', sizeBytes: 15_812 },
    ],
  },
}

export const k6Run = {
  metadata: {
    name: 'k6-smoke-abc',
    uid: 'r-1',
    namespace: 'kubetest-e2e',
    labels: { 'kubetest.io/tool': 'k6' },
  },
  spec: { testRef: 'k6-smoke', source: 'cron' },
  status: {
    phase: 'passed',
    message: '',
    queuedAt: runsFixture[0]!.queuedAt,
    startedAt: runsFixture[0]!.startedAt,
    finishedAt: runsFixture[0]!.finishedAt,
    durationMs: 8_040,
    toolExitCode: 0,
    jobName: 'k6-smoke-abc',
    steps: {},
    counts: { total: 0, passed: 0, failed: 0, skipped: 0 },
    metrics: {},
    artifactRefs: [],
  },
}
