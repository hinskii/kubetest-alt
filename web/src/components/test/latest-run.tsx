import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";

import { runQuery, type TestKey } from "@/api/queries";
import { PhaseBadge } from "@/components/phase-badge";
import { RunFlow } from "@/components/run/run-flow";
import { Skeleton } from "@/components/ui/skeleton";
import { duration, when } from "@/lib/format";

/**
 * LatestRun is the Test's newest run as a card: who, when, how long, and
 * its course (RunFlow), live while it runs (step 20a).
 */
export function LatestRun({ k, id }: { k: TestKey; id: string }) {
  const run = useQuery(runQuery({ cluster: k.cluster, ns: k.ns, id }));
  const navigate = useNavigate();
  if (run.isPending) return <Skeleton className="h-32 w-full" />;
  if (!run.data) return null;
  const r = run.data;
  const params = { cluster: k.cluster, ns: k.ns, id: r.id };
  return (
    <section className="bg-card space-y-4 rounded-xl border p-5" aria-label="Latest run">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-lg font-semibold">
          <Link to="/clusters/$cluster/runs/$ns/$id" params={params} className="hover:underline">
            {r.name}
          </Link>
        </h2>
        <PhaseBadge phase={r.phase} />
      </div>
      <dl className="text-muted-foreground flex flex-wrap gap-x-5 gap-y-1 text-sm">
        {r.createdBy && (
          <div className="flex gap-1">
            <dt>By</dt>
            <dd className="text-foreground">{r.createdBy}</dd>
          </div>
        )}
        {r.startedAt && (
          <div className="flex gap-1">
            <dt>Started</dt>
            <dd className="text-foreground">{when(r.startedAt)}</dd>
          </div>
        )}
        {r.finishedAt && (
          <div className="flex gap-1">
            <dt>Finished</dt>
            <dd className="text-foreground">{when(r.finishedAt)}</dd>
          </div>
        )}
        {r.durationMs ? (
          <div className="flex gap-1">
            <dt>Took</dt>
            <dd className="text-foreground">{duration(r.durationMs)}</dd>
          </div>
        ) : null}
        <Link to="/clusters/$cluster/runs/$ns/$id" params={params} className="text-primary hover:underline">
          Open details &amp; logs
        </Link>
      </dl>
      {r.message && <p className="text-sm">{r.message}</p>}
      <RunFlow
        stages={r.stages}
        onSelect={(stage) =>
          void navigate({ to: "/clusters/$cluster/runs/$ns/$id", params, search: { stage } })
        }
      />
    </section>
  );
}
