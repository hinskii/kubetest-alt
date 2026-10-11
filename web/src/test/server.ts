import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";

import type {
  Cluster,
  Me,
  RunDetail,
  RunRow,
  TestDetail,
  TestFormOf,
  TestSummary,
  ToolTemplate,
} from "@/api/client";

// Default API answers for component tests; a test overrides with
// server.use(...).
export const me: Me = { email: "dev@example.com", role: "developer", can: { run: true, delete: false } };
export const admin: Me = { email: "boss@example.com", role: "admin", can: { run: true, delete: true } };
export const viewer: Me = {
  email: "someone@example.com",
  role: "viewer",
  can: { run: false, delete: false },
};

export const clusters: Cluster[] = [
  { name: "dev", displayName: "Development", reachable: true },
  { name: "prod", displayName: "Production", reachable: false, error: "dial tcp: i/o timeout" },
];

export const tests: TestSummary[] = [
  {
    name: "smoke",
    namespace: "team-a",
    tool: "k6",
    locked: false,
    latestRun: { name: "smoke-abcde", phase: "failed", finishedAt: "2026-10-07T12:00:00Z" },
  },
  { name: "checkout", namespace: "team-b", tool: "cypress", locked: true, problem: "set spec.config.script" },
  { name: "bare", namespace: "team-b", tool: "", locked: false },
];

export const smoke: TestDetail = {
  name: "smoke",
  namespace: "team-a",
  tool: "k6",
  locked: false,
  templates: ["base"],
  params: [
    { name: "env", type: "string", default: "stage", enum: ["stage", "prod"], required: false },
    { name: "target", type: "string", pattern: "^https://", required: true },
    { name: "vus", type: "integer", default: "10", required: false },
  ],
  schedule: { expr: "0 2 * * *", next: ["2026-10-08T02:00:00Z"] },
  definition: "use:\n  - base\ncontainer:\n  image: grafana/k6:1.4.0\n",
};

export const runs: RunRow[] = [
  {
    id: "smoke-git",
    name: "smoke-git",
    phase: "passed",
    startedAt: "2026-10-07T12:01:00Z",
    durationMs: 5600,
    source: "ui",
    createdBy: "dev@example.com",
    commit: { sha: "0123456789abcdef", url: "https://github.com/acme/tests/commit/0123456789abcdef" },
    comment: "baseline",
  },
  { id: "uid-1", name: "smoke-old", phase: "failed", startedAt: "2026-10-07T12:00:00Z", durationMs: 184000 },
];

export const finishedRun: RunDetail = {
  id: "smoke-abcde",
  name: "smoke-abcde",
  namespace: "team-a",
  testRef: "smoke",
  phase: "failed",
  finished: true,
  archived: false,
  tool: "k6",
  message: "1 of 2 workers failed",
  source: "ui",
  createdBy: "dev@example.com",
  startedAt: "2026-10-07T12:00:00Z",
  finishedAt: "2026-10-07T12:00:05Z",
  durationMs: 5600,
  git: {
    repository: "https://github.com/acme/tests.git",
    revision: "main",
    commit: { sha: "0123456789abcdef", url: "https://github.com/acme/tests/commit/0123456789abcdef" },
  },
  testCounts: { total: 5, passed: 3, failed: 2, skipped: 0 },
  config: { vus: "50" },
  metrics: [{ name: "p95_ms", value: 120.5678 }],
  steps: [
    { key: "worker-2", kind: "Worker", phase: "failed", message: "exit code 1" },
    { key: "worker-10", kind: "Worker", phase: "passed" },
  ],
  children: [],
  report: "report/index.html",
  artifacts: [
    { path: "report/index.html", contentType: "text/html" },
    { path: "shots/pay (failed).png", contentType: "image/png" },
  ],
  media: [{ path: "shots/pay (failed).png", type: "image/png", video: false }],
  events: [],
  failedCases: [
    {
      key: "shop › checkout",
      name: "checkout",
      class: "shop",
      status: "failed",
      message: "timeout after 5s",
      details: "at checkout",
    },
  ],
};

// smoke as the editor shows it (Settings), and the namespace's catalog.
export const smokeForm: TestFormOf = {
  locked: false,
  form: {
    namespace: "team-a",
    name: "smoke",
    template: "k6",
    tool: "k6",
    source: "git",
    gitUri: "https://github.com/acme/perf",
    gitPath: "perf/smoke.js",
    gitRevision: "main",
    params: { vus: "20" },
    timeout: "15m",
    memoryLimit: "1Gi",
    resourceVersion: "41",
    mode: "form",
    yaml: "apiVersion: tests.kubetest.io/v1alpha1\nkind: Test\nmetadata:\n  name: smoke\n",
  },
};

export const k6Template: ToolTemplate = {
  name: "k6",
  tool: "k6",
  image: "grafana/k6:1.4.0",
  mainParam: "script",
  mainKind: "file",
  params: [
    { name: "script", type: "string", required: false, description: "The k6 script, e.g. perf/checkout.js." },
    { name: "vus", type: "integer", default: "10", required: false },
  ],
};

const T = "*/api/v1/clusters/dev/tests/team-a/smoke";
const R = "*/api/v1/clusters/dev/runs/:ns/:id";

export const server = setupServer(
  http.get("*/api/v1/me", () => HttpResponse.json(me)),
  http.get("*/api/v1/clusters", () => HttpResponse.json(clusters)),
  http.get("*/api/v1/clusters/dev/tests", () => HttpResponse.json({ tests })),
  http.get(T, () => HttpResponse.json(smoke)),
  http.get(`${T}/runs`, () => HttpResponse.json({ runs })),
  http.get(`${T}/form`, () => HttpResponse.json(smokeForm)),
  http.get("*/api/v1/clusters/dev/templates", () => HttpResponse.json({ templates: [k6Template] })),
  http.get("*/api/v1/clusters/dev/schedules", () =>
    HttpResponse.json({
      recurring: [
        {
          name: "nightly",
          namespace: "team-a",
          tool: "k6",
          locked: false,
          schedule: { expr: "0 2 * * *", next: ["2026-10-08T02:00:00Z", "2026-10-09T02:00:00Z"] },
          latestRun: { name: "nightly-x", phase: "passed", finishedAt: "2026-10-07T02:00:05Z" },
        },
        {
          name: "broken",
          namespace: "team-b",
          tool: "",
          locked: true,
          schedule: { expr: "61 * * * *", next: [], error: "end of range (61) above maximum (59): 61" },
        },
      ],
      oneTime: [
        {
          id: "smoke-later",
          name: "smoke-later",
          namespace: "team-a",
          testRef: "smoke",
          notBefore: "2030-01-02T03:04:00Z",
          createdBy: "dev@example.com",
        },
      ],
    }),
  ),
  http.get(R, ({ params }) => HttpResponse.json({ ...finishedRun, id: params.id, name: params.id })),
  http.get(`${R}/log`, () =>
    HttpResponse.text("hello\nworld\n", { headers: { "X-Run-Phase": "failed", "X-Next-Offset": "12" } }),
  ),
);
