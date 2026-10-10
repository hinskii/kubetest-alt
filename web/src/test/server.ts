import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";

import type { Cluster, Me } from "@/api/client";

// Default API answers for component tests; a test overrides with
// server.use(...).
export const me: Me = { email: "dev@example.com", role: "developer", can: { run: true, delete: false } };
export const clusters: Cluster[] = [
  { name: "dev", displayName: "Development", reachable: true },
  { name: "prod", displayName: "Production", reachable: false, error: "dial tcp: i/o timeout" },
];

export const server = setupServer(
  http.get("*/api/v1/me", () => HttpResponse.json(me)),
  http.get("*/api/v1/clusters", () => HttpResponse.json(clusters)),
);
