import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { z } from "zod";

import { caseHistoryQuery, clustersQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { PhaseBadge } from "@/components/phase-badge";
import { Panel } from "@/components/run/sections";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { duration, when } from "@/lib/format";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/clusters/$cluster/tests/$ns/$name_/cases_/history")({
  validateSearch: z.object({ case: z.string().catch("") }),
  component: CaseHistoryPage,
});

const dot: Record<string, string> = {
  passed: "bg-success",
  failed: "bg-destructive",
  error: "bg-destructive",
  skipped: "bg-unknown",
};

function CaseHistoryPage() {
  const k = Route.useParams();
  const { case: key } = Route.useSearch();
  const clusters = useQuery(clustersQuery);
  const clusterTitle = clusters.data?.find((c) => c.name === k.cluster)?.displayName ?? k.cluster;
  const history = useQuery({ ...caseHistoryQuery(k, key), enabled: key !== "" });

  return (
    <div className="space-y-6">
      <PageHeader
        crumbs={[
          { label: "Clusters", to: "/" },
          { label: clusterTitle, to: "/clusters/$cluster", params: { cluster: k.cluster } },
          { label: k.name, to: "/clusters/$cluster/tests/$ns/$name", params: k },
          { label: "Test cases", to: "/clusters/$cluster/tests/$ns/$name/cases", params: k },
          { label: key || "?" },
        ]}
        title={key || "Which test case?"}
      />
      {key === "" && <p className="text-muted-foreground">Pick a test case from the list.</p>}
      {history.isError && <ErrorView error={history.error} />}
      {history.isPending && key !== "" && <Skeleton className="h-32 w-full" />}
      {history.data && (
        <>
          <p className="text-muted-foreground -mt-4 text-sm" data-testid="history-summary">
            {k.name} · {history.data.runs.length} runs · {history.data.passed} passed · {history.data.failed}{" "}
            failed · {history.data.skipped} skipped
          </p>
          {history.data.unavailable ? (
            <p className="text-muted-foreground" role="status">
              A case&apos;s history comes from run history, which this cluster doesn&apos;t keep (
              {history.data.unavailable}).
            </p>
          ) : history.data.runs.length === 0 ? (
            <p className="text-muted-foreground">No runs recorded this test case.</p>
          ) : (
            <Panel id="history" title="Latest first">
              <div className="flex flex-wrap gap-1" aria-label="Result per run, newest first">
                {history.data.runs.map((r) => (
                  <Link
                    key={r.runId}
                    to="/clusters/$cluster/runs/$ns/$id"
                    params={{ cluster: k.cluster, ns: k.ns, id: r.runId }}
                    title={`${r.runName || r.runId} · ${r.status} · ${when(r.finishedAt)}`}
                    aria-label={`${r.runName || r.runId}: ${r.status}`}
                    className={cn("size-3 rounded-full", dot[r.status] ?? "bg-unknown")}
                  />
                ))}
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Run</TableHead>
                    <TableHead>Result</TableHead>
                    <TableHead>Finished</TableHead>
                    <TableHead>Duration</TableHead>
                    <TableHead>Message</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {history.data.runs.map((r) => (
                    <TableRow key={r.runId}>
                      <TableCell>
                        <Link
                          to="/clusters/$cluster/runs/$ns/$id"
                          params={{ cluster: k.cluster, ns: k.ns, id: r.runId }}
                          className="text-primary hover:underline"
                        >
                          {r.runName || r.runId}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <PhaseBadge phase={r.status} />
                      </TableCell>
                      <TableCell className="text-muted-foreground">{when(r.finishedAt)}</TableCell>
                      <TableCell>{duration(r.durationMs)}</TableCell>
                      <TableCell className="whitespace-normal">{r.message}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </Panel>
          )}
        </>
      )}
    </div>
  );
}
