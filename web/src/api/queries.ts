import { keepPreviousData, queryOptions } from "@tanstack/react-query";

import { ApiError, api, unwrap, type TestForm } from "./client";

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

export type RunPhase = "queued" | "running" | "passed" | "failed" | "error" | "aborted";

export const testRunsQuery = (k: TestKey, after?: string, phase?: RunPhase) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name, "runs", after ?? "", phase ?? ""],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}/runs", {
          params: { ...testParams(k), query: { ...(after ? { after } : {}), ...(phase ? { phase } : {}) } },
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
    // Every 2 s while the run goes: its stages (RunFlow) move live.
    refetchInterval: (q) => (q.state.data?.finished ? false : 2_000),
  });

/** A chunk of a run's log from offset on, with the run's phase. */
export async function runLogChunk(k: RunKey, offset: number, signal?: AbortSignal, stage?: "fetch") {
  const r = await api.GET("/api/v1/clusters/{cluster}/runs/{ns}/{id}/log", {
    params: { ...runParams(k), query: stage ? { offset, stage } : { offset } },
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

export type CleanupCriteria = { mode: "older" | "range"; days?: string; from?: string; to?: string };

export const cleanupQuery = (k: TestKey, c: CleanupCriteria) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name, "cleanup", c],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}/cleanup", {
          params: { ...testParams(k), query: c },
        }),
      ),
    retry: false,
  });

export const cleanup = async (k: TestKey, runs: string[]) =>
  unwrap(
    await api.POST("/api/v1/clusters/{cluster}/tests/{ns}/{name}/cleanup", {
      params: testParams(k),
      body: { runs },
    }),
  );

/** The run windows the case list and analytics offer. */
export type Window = 10 | 30 | 100;
export const toWindow = (n?: number): Window => (n === 10 || n === 100 ? n : 30);

export const casesQuery = (k: TestKey, runs: Window) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name, "cases", runs],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}/cases", {
          params: { ...testParams(k), query: { runs } },
        }),
      ),
  });

export const caseHistoryQuery = (k: TestKey, key: string) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name, "cases", "history", key],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}/cases/history", {
          params: { ...testParams(k), query: { case: key } },
        }),
      ),
  });

export const compareRunQuery = (k: RunKey, withRun?: string) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "runs", k.ns, k.id, "compare", withRun ?? ""],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/runs/{ns}/{id}/compare", {
          params: { ...runParams(k), query: withRun ? { with: withRun } : {} },
        }),
      ),
  });

/** testOverviewQuery is a Test's finished runs over a range: the Overview tiles. */
export const testOverviewQuery = (k: TestKey, range: "24h" | "7d" | "30d") =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name, "overview", range],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}/overview", {
          params: { ...testParams(k), query: { range } },
        }),
      ),
    refetchInterval: 30_000,
  });

export const analyticsQuery = (k: TestKey, runs: Window) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name, "analytics", runs],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}/analytics", {
          params: { ...testParams(k), query: { runs } },
        }),
      ),
  });

export const comparisonQuery = (k: TestKey, run: string[]) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name, "analytics", "compare", run],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}/analytics/compare", {
          params: { ...testParams(k), query: { run } },
        }),
      ),
  });

export const namespacesQuery = (cluster: string) =>
  queryOptions({
    queryKey: ["clusters", cluster, "namespaces"],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/clusters/{cluster}/namespaces", { params: { path: { cluster } } })),
    staleTime: 60_000,
  });

export const templatesQuery = (cluster: string, namespace: string) =>
  queryOptions({
    queryKey: ["clusters", cluster, "templates", namespace],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster}/templates", {
          params: { path: { cluster }, query: { namespace } },
        }),
      ),
    staleTime: 60_000,
  });

export const testFormQuery = (k: TestKey) =>
  queryOptions({
    queryKey: ["clusters", k.cluster, "tests", k.ns, k.name, "form"],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/clusters/{cluster}/tests/{ns}/{name}/form", { params: testParams(k) })),
  });

/**
 * previewTest is the YAML a form describes (the save's own build). A form
 * that can't be built at all has no YAML, only its errors: the page keeps
 * its last YAML and shows them.
 */
export async function previewTest(cluster: string, form: TestForm, edit?: TestKey, signal?: AbortSignal) {
  const r = edit
    ? await api.POST("/api/v1/clusters/{cluster}/tests/{ns}/{name}/preview", {
        params: testParams(edit),
        body: form,
        signal,
      })
    : await api.POST("/api/v1/clusters/{cluster}/tests/preview", {
        params: { path: { cluster } },
        body: form,
        signal,
      });
  return unwrap(r);
}

/** FormRefused is a form the API wouldn't save, with every reason. */
export class FormRefused extends ApiError {
  constructor(
    status: number,
    message: string,
    readonly errors: string[],
  ) {
    super(status, message);
  }
}

function refused(r: { error?: unknown; response: Response }): never {
  const e = r.error as { error?: string; errors?: string[] } | undefined;
  throw new FormRefused(
    r.response.status,
    e?.error ?? r.response.statusText,
    e?.errors ?? (e?.error ? [e.error] : []),
  );
}

export async function createTest(cluster: string, form: TestForm, dryRun = false) {
  const r = await api.POST("/api/v1/clusters/{cluster}/tests", {
    params: { path: { cluster }, query: dryRun ? { "dry-run": true } : {} },
    body: form,
  });
  return r.data ?? refused(r);
}

export async function updateTest(k: TestKey, form: TestForm, dryRun = false) {
  const r = await api.PUT("/api/v1/clusters/{cluster}/tests/{ns}/{name}", {
    params: { ...testParams(k), query: dryRun ? { "dry-run": true } : {} },
    body: form,
  });
  return r.data ?? refused(r);
}
