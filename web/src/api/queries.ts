import { keepPreviousData, queryOptions } from "@tanstack/react-query";

import { api, unwrap } from "./client";

/** Lists refresh every 10 s while the tab is visible (as the Go pages did). */
export const REFRESH_MS = 10_000;

export const meQuery = queryOptions({
  queryKey: ["me"],
  queryFn: async () => unwrap(await api.GET("/api/v1/me")),
  staleTime: 5 * 60_000,
});

export const clustersQuery = queryOptions({
  queryKey: ["clusters"],
  queryFn: async () => unwrap(await api.GET("/api/v1/clusters")),
  refetchInterval: 30_000,
});

export const testsQuery = (cluster: string) =>
  queryOptions({
    queryKey: ["clusters", cluster, "tests"],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/clusters/{cluster}/tests", { params: { path: { cluster } } })),
    refetchInterval: REFRESH_MS,
  });

export type TestKey = { cluster: string; ns: string; name: string };

const testParams = ({ cluster, ns, name }: TestKey) => ({ path: { cluster, ns, name } });

export const testQuery = (k: TestKey) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}", { params: testParams(k) })),
  });

export const testRunsQuery = (k: TestKey, after?: string) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name, "runs", after ?? ""],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}/runs", {
          params: { ...testParams(k), query: after ? { after } : {} },
        }),
      ),
    refetchInterval: REFRESH_MS,
    placeholderData: keepPreviousData,
  });

export async function startRun(k: TestKey, body: { config: Record<string, string>; notBefore?: string }) {
  return unwrap(
    await api.POST("/api/v1/clusters/{cluster}/tests/{ns}/{name}/runs", { params: testParams(k), body }),
  );
}

export async function setSchedule(k: TestKey, schedule: string) {
  return unwrap(
    await api.PUT("/api/v1/clusters/{cluster}/tests/{ns}/{name}/schedule", {
      params: testParams(k),
      body: { schedule },
    }),
  );
}

export async function deleteTest(k: TestKey) {
  const r = await api.DELETE("/api/v1/clusters/{cluster}/tests/{ns}/{name}", { params: testParams(k) });
  if (r.response.status !== 204) unwrap(r);
}
