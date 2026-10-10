import { queryOptions } from "@tanstack/react-query";

import { api, unwrap } from "./client";

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
