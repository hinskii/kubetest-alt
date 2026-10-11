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

export const schedulesQuery = (cluster: string) =>
  queryOptions({
    queryKey: ["clusters", cluster, "schedules"],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/clusters/{cluster}/schedules", { params: { path: { cluster } } })),
    refetchInterval: REFRESH_MS,
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
  return noContent(
    await api.DELETE("/api/v1/clusters/{cluster}/tests/{ns}/{name}", { params: testParams(k) }),
  );
}

export type RunKey = { cluster: string; ns: string; id: string };

const runParams = ({ cluster, ns, id }: RunKey) => ({ path: { cluster, ns, id } });

/** A run refreshes every 5 s until it has finished. */
export const runQuery = (k: RunKey) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "runs", k.ns, k.id],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/clusters/{cluster}/runs/{ns}/{id}", { params: runParams(k) })),
    refetchInterval: (q) => (q.state.data?.finished ? false : 5_000),
  });

/** A chunk of a run's log from offset on, with the run's phase. */
export async function runLogChunk(k: RunKey, offset: number, signal?: AbortSignal) {
  const r = await api.GET("/api/v1/clusters/{cluster}/runs/{ns}/{id}/log", {
    params: { ...runParams(k), query: { offset } },
    parseAs: "text",
    signal,
  });
  // An empty chunk (nothing new yet) has no body, so no data: still a success.
  const text = r.response.ok ? (r.data ?? "") : unwrap(r);
  const h = r.response.headers;
  return {
    text,
    phase: h.get("X-Run-Phase") ?? "",
    next: Number(h.get("X-Next-Offset") ?? offset),
    more: h.get("X-More") === "1",
  };
}

async function noContent(r: { response: Response; error?: unknown }) {
  if (r.response.status !== 204) unwrap(r as { data?: unknown; error?: unknown; response: Response });
}

export const abortRun = async (k: RunKey, message: string) =>
  noContent(
    await api.POST("/api/v1/clusters/{cluster}/runs/{ns}/{id}/abort", {
      params: runParams(k),
      body: { message },
    }),
  );

export const rerun = async (k: RunKey) =>
  unwrap(await api.POST("/api/v1/clusters/{cluster}/runs/{ns}/{id}/rerun", { params: runParams(k) }));

export const setComment = async (k: RunKey, text: string) =>
  unwrap(
    await api.PUT("/api/v1/clusters/{cluster}/runs/{ns}/{id}/comment", {
      params: runParams(k),
      body: { text },
    }),
  );

export const deleteComment = async (k: RunKey) =>
  noContent(await api.DELETE("/api/v1/clusters/{cluster}/runs/{ns}/{id}/comment", { params: runParams(k) }));

export const deleteRun = async (k: RunKey) =>
  noContent(await api.DELETE("/api/v1/clusters/{cluster}/runs/{ns}/{id}", { params: runParams(k) }));
