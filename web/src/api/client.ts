import createClient from "openapi-fetch";

import type { components, paths } from "./schema";

// Typed client for Control Center's /api/v1 (openapi/control-center.json →
// schema.d.ts via `pnpm gen:api`). Same origin: the sign-in proxy's cookie
// rides along, the browser never talks to a cluster.
// fetch is looked up per call, not captured once, so test mocks (MSW)
// installed after this module loads still apply.
export const api = createClient<paths>({
  baseUrl: window.location.origin,
  fetch: (request) => globalThis.fetch(request),
});

export type Me = components["schemas"]["Me"];
export type Cluster = components["schemas"]["Cluster"];
export type TestSummary = components["schemas"]["TestSummary"];
export type TestDetail = components["schemas"]["TestDetail"];
export type Param = components["schemas"]["Param"];
export type RunRow = components["schemas"]["RunRow"];
export type Schedule = components["schemas"]["Schedule"];
export type RunDetail = components["schemas"]["RunDetail"];
export type RunEvent = components["schemas"]["Event"];
export type CaseStat = components["schemas"]["CaseStat"];
export type CaseDiff = components["schemas"]["CaseDiff"];
export type Trend = components["schemas"]["Trend"];
export type Analytics = components["schemas"]["Analytics"];
export type Comparison = components["schemas"]["Comparison"];
export type TestForm = components["schemas"]["TestForm"];
export type ToolTemplate = components["schemas"]["ToolTemplate"];
export type Preview = components["schemas"]["Preview"];

/** ApiError carries the API's `{error}` message and the HTTP status. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** unwrap turns an openapi-fetch result into data or a thrown ApiError. */
export function unwrap<T>(r: { data?: T; error?: unknown; response: Response }): T {
  if (r.data !== undefined) return r.data;
  const msg =
    typeof r.error === "object" && r.error && "error" in r.error
      ? String((r.error as { error: unknown }).error)
      : `${r.response.status} ${r.response.statusText}`;
  throw new ApiError(r.response.status, msg);
}
