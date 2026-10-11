import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { toast } from "sonner";

import { abortRun, clustersQuery, meQuery, schedulesQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { Chip, PhaseBadge } from "@/components/phase-badge";
import { Panel } from "@/components/run/sections";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { when } from "@/lib/format";

export const Route = createFileRoute("/clusters/$cluster/schedules")({ component: SchedulesPage });

function SchedulesPage() {
  const { cluster } = Route.useParams();
  const schedules = useQuery(schedulesQuery(cluster));
  const me = useQuery(meQuery);
  const clusters = useQuery(clustersQuery);
  const qc = useQueryClient();
  const title = clusters.data?.find((c) => c.name === cluster)?.displayName ?? cluster;
  const cancel = useMutation({
    mutationFn: (r: { ns: string; id: string }) =>
      abortRun({ cluster, ...r }, "cancelled before its start time"),
    onSuccess: () => {
      toast.success("Run cancelled.");
      return qc.invalidateQueries({ queryKey: schedulesQuery(cluster).queryKey });
    },
    onError: (e) => toast.error(`Could not cancel: ${e.message}`),
  });

  return (
    <div className="space-y-6">
      <PageHeader
        crumbs={[
          { label: "Clusters", to: "/" },
          { label: title, to: "/clusters/$cluster", params: { cluster } },
          { label: "Schedules" },
        ]}
        title="Schedules"
      />
      <p className="text-muted-foreground -mt-4 text-sm">
        Recurring runs come from a Test&apos;s <code className="font-mono">spec.schedule</code> (cron, UTC
        unless it starts with <code className="font-mono">CRON_TZ=</code>); one-time runs wait for their start
        time. Times are UTC.
      </p>
      {schedules.isError && <ErrorView error={schedules.error} />}
      {schedules.isPending && <Skeleton className="h-40 w-full" />}
      {schedules.data && (
        <>
          <Panel
            id="recurring"
            title={
              <>
                Recurring{" "}
                <span className="text-muted-foreground text-sm font-normal">
                  {schedules.data.recurring.length}
                </span>
              </>
            }
          >
            {schedules.data.recurring.length === 0 ? (
              <p className="text-muted-foreground">
                No Test has a recurring schedule. Set one on a Test&apos;s page (Tests created in Control
                Center) or in its <code className="font-mono">spec.schedule</code>.
              </p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Test</TableHead>
                    <TableHead>Schedule</TableHead>
                    <TableHead>Next runs</TableHead>
                    <TableHead>Last run</TableHead>
                    <TableHead />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {schedules.data.recurring.map((s) => (
                    <TableRow key={`${s.namespace}/${s.name}`}>
                      <TableCell className="space-x-2">
                        <Link
                          to="/clusters/$cluster/tests/$ns/$name"
                          params={{ cluster, ns: s.namespace, name: s.name }}
                          className="text-primary font-medium hover:underline"
                        >
                          {s.name}
                        </Link>
                        <span className="text-muted-foreground">{s.namespace}</span>
                        {s.tool && <Chip>{s.tool}</Chip>}
                      </TableCell>
                      <TableCell className="font-mono">{s.schedule.expr}</TableCell>
                      <TableCell>
                        {s.schedule.error ? (
                          <span>
                            <PhaseBadge phase="error" className="mr-2" />
                            <span className="text-muted-foreground text-xs">{s.schedule.error}</span>
                          </span>
                        ) : (
                          s.schedule.next.map((n, i) => (
                            <div key={n} className={i ? "text-muted-foreground" : undefined}>
                              {when(n)}
                            </div>
                          ))
                        )}
                      </TableCell>
                      <TableCell>
                        {s.latestRun ? (
                          <span className="flex items-center gap-2">
                            <Link
                              to="/clusters/$cluster/runs/$ns/$id"
                              params={{ cluster, ns: s.namespace, id: s.latestRun.name }}
                              aria-label={`Latest run: ${s.latestRun.phase}`}
                            >
                              <PhaseBadge phase={s.latestRun.phase} />
                            </Link>
                            <span className="text-muted-foreground text-xs">
                              {when(s.latestRun.finishedAt)}
                            </span>
                          </span>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {s.locked && <Chip title="Defined in Git: edit spec.schedule there">read-only</Chip>}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </Panel>

          <Panel
            id="one-time"
            title={
              <>
                One-time{" "}
                <span className="text-muted-foreground text-sm font-normal">
                  {schedules.data.oneTime.length}
                </span>
              </>
            }
          >
            {schedules.data.oneTimeError && (
              <p className="text-muted-foreground">
                Scheduled runs unavailable: {schedules.data.oneTimeError}
              </p>
            )}
            {schedules.data.oneTime.length > 0 ? (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Starts</TableHead>
                    <TableHead>Run</TableHead>
                    <TableHead>Test</TableHead>
                    <TableHead>Requested by</TableHead>
                    <TableHead />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {schedules.data.oneTime.map((r) => (
                    <TableRow key={r.id}>
                      <TableCell>{when(r.notBefore)}</TableCell>
                      <TableCell>
                        <Link
                          to="/clusters/$cluster/runs/$ns/$id"
                          params={{ cluster, ns: r.namespace, id: r.id }}
                          className="text-primary hover:underline"
                        >
                          {r.name}
                        </Link>
                      </TableCell>
                      <TableCell className="space-x-2">
                        <Link
                          to="/clusters/$cluster/tests/$ns/$name"
                          params={{ cluster, ns: r.namespace, name: r.testRef }}
                          className="text-primary hover:underline"
                        >
                          {r.testRef}
                        </Link>
                        <span className="text-muted-foreground">{r.namespace}</span>
                      </TableCell>
                      <TableCell className="text-muted-foreground">{r.createdBy}</TableCell>
                      <TableCell>
                        {me.data?.can.run && (
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={cancel.isPending}
                            aria-label={`Cancel ${r.name}`}
                            onClick={() => cancel.mutate({ ns: r.namespace, id: r.id })}
                          >
                            Cancel
                          </Button>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ) : (
              !schedules.data.oneTimeError && (
                <p className="text-muted-foreground">
                  No runs waiting for a start time. Pick “Start at” in a Test&apos;s run form to schedule one.
                </p>
              )
            )}
          </Panel>
        </>
      )}
    </div>
  );
}
