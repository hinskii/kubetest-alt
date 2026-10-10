import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";

import type { Cluster } from "@/api/client";
import { clustersQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

export const Route = createFileRoute("/")({ component: Home });

function Home() {
  const clusters = useQuery(clustersQuery);
  return (
    <section className="space-y-4">
      <h1 className="text-2xl font-semibold tracking-tight">Clusters</h1>
      {clusters.isPending && <Skeleton className="h-28 w-full" />}
      {clusters.isError && <ErrorView error={clusters.error} />}
      {clusters.data?.length === 0 && (
        <p className="text-muted-foreground">
          No clusters are configured. Add one to Control Center's config.
        </p>
      )}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {clusters.data?.map((c) => (
          <ClusterCard key={c.name} cluster={c} />
        ))}
      </div>
    </section>
  );
}

function ClusterCard({ cluster }: { cluster: Cluster }) {
  // Cluster pages are still server-rendered (step 20R strangler): a plain
  // link, a full page load.
  const href = `/clusters/${encodeURIComponent(cluster.name)}`;
  return (
    <a
      href={href}
      className="group rounded-xl focus-visible:outline-2"
      data-testid={`cluster-${cluster.name}`}
    >
      <Card className="transition-shadow group-hover:shadow-md">
        <CardHeader>
          <CardTitle className="flex items-center justify-between gap-2">
            {cluster.displayName}
            {cluster.reachable ? (
              <Badge className="bg-success-soft text-success">connected</Badge>
            ) : (
              <Badge className="bg-danger-soft text-destructive">unreachable</Badge>
            )}
          </CardTitle>
          <CardDescription className="font-mono">{cluster.name}</CardDescription>
        </CardHeader>
        {cluster.error && (
          <CardContent className="text-sm text-destructive break-words">{cluster.error}</CardContent>
        )}
      </Card>
    </a>
  );
}
