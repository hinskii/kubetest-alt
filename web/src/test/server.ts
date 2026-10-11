import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";

import type { Cluster, Me, RunRow, TestDetail, TestSummary } from "@/api/client";

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

const T = "*/api/v1/clusters/dev/tests/team-a/smoke";

export const server = setupServer(
  http.get("*/api/v1/me", () => HttpResponse.json(me)),
  http.get("*/api/v1/clusters", () => HttpResponse.json(clusters)),
  http.get("*/api/v1/clusters/dev/tests", () => HttpResponse.json({ tests })),
  http.get(T, () => HttpResponse.json(smoke)),
  http.get(`${T}/runs`, () => HttpResponse.json({ runs })),
);
