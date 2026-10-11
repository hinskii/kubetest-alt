import { useQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { z } from "zod";

import type { CaseDiff } from "@/api/client";
import { clustersQuery, compareRunQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { PhaseBadge } from "@/components/phase-badge";
import { Panel } from "@/components/run/sections";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { when } from "@/lib/format";

export const Route = createFileRoute("/clusters/$cluster/runs/$ns/$id_/compare")({
  validateSearch: z.object({ with: z.string().optional().catch(undefined) }),
  component: ComparePage,
});

/** deltaMs renders a duration change: "+600ms", "-1.2s". */
function deltaMs(ms?: number): string {
  if (!ms) return "";
  const sign = ms > 0 ? "+" : "-";
  const a = Math.abs(ms);
  return a < 1000 ? `${sign}${a}ms` : `${sign}${(a / 1000).toFixed(1)}s`;
}

function DiffSection({ id, title, rows }: { id: string; title: string; rows: CaseDiff[] }) {
  if (rows.length === 0) return null;
  return (
    <Panel
      id={id}
      title={
        <>
          {title} <span className="text-muted-foreground text-sm font-normal">{rows.length}</span>
        </>
      }
    >
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Test case</TableHead>
            <TableHead>Before</TableHead>
            <TableHead>Now</TableHead>
            <TableHead>Duration change</TableHead>
            <TableHead>Message</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r) => (
            <TableRow key={r.key}>
              <TableCell>{r.key}</TableCell>
              <TableCell>
                {r.before ? (
                  <PhaseBadge phase={r.before} />
                ) : (
                  <span className="text-muted-foreground">—</span>
                )}
              </TableCell>
              <TableCell>
                {r.now ? <PhaseBadge phase={r.now} /> : <span className="text-muted-foreground">—</span>}
              </TableCell>
              <TableCell className="text-muted-foreground">{deltaMs(r.durationDiffMs)}</TableCell>
              <TableCell className="whitespace-normal">{r.message}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Panel>
  );
}

function ComparePage() {
  const k = Route.useParams();
  const { with: withRun } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const clusters = useQuery(clustersQuery);
  const clusterTitle = clusters.data?.find((c) => c.name === k.cluster)?.displayName ?? k.cluster;
  const cmp = useQuery(compareRunQuery(k, withRun));
  const d = cmp.data;

  return (
    <div className="space-y-6">
      <PageHeader
        crumbs={[
          { label: "Clusters", to: "/" },
          { label: clusterTitle, to: "/clusters/$cluster", params: { cluster: k.cluster } },
          ...(d
            ? [
                {
                  label: d.testRef,
                  to: "/clusters/$cluster/tests/$ns/$name",
                  params: { cluster: k.cluster, ns: k.ns, name: d.testRef },
                },
              ]
            : []),
          { label: d?.run.name ?? k.id, to: "/clusters/$cluster/runs/$ns/$id", params: k },
          { label: "Compare" },
        ]}
        title={`Compare ${d?.run.name ?? k.id}`}
      />
      {cmp.isError && <ErrorView error={cmp.error} />}
      {cmp.isPending && <Skeleton className="h-32 w-full" />}
      {d && (
        <>
          {d.candidates.length === 0 ? (
            <p className="text-muted-foreground">No other finished run of {d.testRef} to compare with.</p>
          ) : (
            <div className="grid max-w-xl gap-1.5">
              <Label htmlFor="with">with an earlier run</Label>
              <Select value={withRun ?? ""} onValueChange={(v) => navigate({ search: { with: v } })}>
                <SelectTrigger id="with" className="bg-card w-full">
                  <SelectValue placeholder="Pick a run…" />
                </SelectTrigger>
                <SelectContent>
                  {d.candidates.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name} · {c.phase} · {when(c.finishedAt)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          {d.base && (
            <>
              <p className="text-muted-foreground" data-testid="compare-summary">
                {d.run.name} ({d.run.phase}, {d.runCases} cases) against {d.base.name} ({d.base.phase},{" "}
                {d.baseCases} cases)
              </p>
              {d.casesError ? (
                <p className="text-muted-foreground">Test cases unavailable: {d.casesError}</p>
              ) : (
                <>
                  <DiffSection id="started" title="Started failing" rows={d.startedFailing} />
                  <DiffSection id="still" title="Still failing" rows={d.stillFailing} />
                  <DiffSection id="fixed" title="Fixed" rows={d.fixed} />
                  <DiffSection id="added" title="New tests" rows={d.added} />
                  <DiffSection id="removed" title="No longer run" rows={d.removed} />
                  <p className="text-muted-foreground">Unchanged: {d.unchanged}</p>
                </>
              )}
            </>
          )}
        </>
      )}
    </div>
  );
}
