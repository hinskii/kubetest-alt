import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";

import { clustersQuery, meQuery, runQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader, type Crumb } from "@/components/page-header";
import { Chip, PhaseBadge } from "@/components/phase-badge";
import { Comment } from "@/components/run/comment-form";
import { AbortRun, DeleteRun, RunAgain } from "@/components/run/run-actions";
import { LogDownload, RunLog } from "@/components/run/run-log";
import {
  Activity,
  Artifacts,
  Children,
  Events,
  FailedCases,
  Gallery,
  Panel,
  RunFacts,
  Steps,
} from "@/components/run/sections";
import { ToolFrame, useFrameColors } from "@/components/run/tool-frame";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { artifactUrl, goPage } from "@/lib/paths";

export const Route = createFileRoute("/clusters/$cluster/runs/$ns/$id")({ component: RunPage });

function RunPage() {
  const k = Route.useParams();
  const run = useQuery(runQuery(k));
  const me = useQuery(meQuery);
  const clusters = useQuery(clustersQuery);
  const frames = useFrameColors();
  // The live link is signed anew on every refresh: keep the first, or
  // the frame would reload each time.
  const [liveView, setLiveView] = useState<string>();
  if (!liveView && run.data?.liveView) setLiveView(run.data.liveView);

  const clusterTitle = clusters.data?.find((c) => c.name === k.cluster)?.displayName ?? k.cluster;
  const crumbs: Crumb[] = [
    { label: "Clusters", to: "/" },
    { label: clusterTitle, to: "/clusters/$cluster", params: { cluster: k.cluster } },
    ...(run.data
      ? [
          {
            label: run.data.testRef,
            to: "/clusters/$cluster/tests/$ns/$name",
            params: { cluster: k.cluster, ns: k.ns, name: run.data.testRef },
          },
        ]
      : []),
    { label: run.data?.name ?? k.id },
  ];

  if (run.isError) {
    return (
      <>
        <PageHeader crumbs={crumbs} title={k.id} />
        <ErrorView error={run.error} />
      </>
    );
  }
  if (!run.data) return <Skeleton className="h-64 w-full" />;
  const r = run.data;
  const canRun = !!me.data?.can.run;
  const report = r.report ? artifactUrl(k.cluster, k.ns, r.id, r.report) : undefined;
  const activity = !r.finished ? r.events.at(-1) : undefined;

  return (
    <div className="space-y-6">
      <PageHeader
        crumbs={crumbs}
        title={
          <span className="flex flex-wrap items-center gap-2">
            {r.name}
            <PhaseBadge phase={r.phase} className="text-sm" />
            {r.tool && <Chip>{r.tool}</Chip>}
          </span>
        }
      >
        {report && (
          <Button asChild>
            <a href={report} target="_blank" rel="noopener noreferrer">
              Open report
            </a>
          </Button>
        )}
        {r.finished && (
          <Button variant="outline" asChild>
            <a href={goPage.compare(k.cluster, k.ns, r.id)}>Compare with another run</a>
          </Button>
        )}
        {canRun && <RunAgain k={k} name={r.name} testRef={r.testRef} />}
      </PageHeader>

      {r.message && <p className="-mt-2">{r.message}</p>}
      {activity && <Activity event={activity} />}

      <section className="bg-card rounded-xl border p-5" aria-label="Facts">
        <RunFacts cluster={k.cluster} run={r} />
      </section>

      {r.children.length > 0 && (
        <Panel
          id="children"
          title={
            <>
              Child runs{" "}
              <span className="text-muted-foreground text-sm font-normal">{r.children.length}</span>
            </>
          }
        >
          <Children cluster={k.cluster} run={r} />
        </Panel>
      )}

      {!r.finished && liveView && (
        <Panel
          id="live"
          title="Live view"
          actions={
            <>
              {frames.button}
              <Button variant="outline" size="sm" asChild>
                <a href={liveView} target="_blank" rel="noopener noreferrer">
                  Open in a new tab
                </a>
              </Button>
            </>
          }
        >
          <p className="text-muted-foreground text-sm">
            The tool&apos;s own dashboard, live while the run is going. It runs isolated from Control Center.
          </p>
          <ToolFrame src={liveView} title={`Live view of ${r.name}`} original={frames.original} />
        </Panel>
      )}

      {r.finished && report && (
        <Panel
          id="report"
          title="Report"
          actions={
            <>
              {frames.button}
              <Button variant="outline" size="sm" asChild>
                <a href={report} target="_blank" rel="noopener noreferrer">
                  Open in a new tab
                </a>
              </Button>
              <Button variant="outline" size="sm" asChild>
                <a href={artifactUrl(k.cluster, k.ns, r.id, r.report!, true)}>Download</a>
              </Button>
            </>
          }
        >
          <p className="text-muted-foreground text-sm">
            The tool&apos;s report of this run. It runs isolated from Control Center.
          </p>
          <ToolFrame src={report} title={`Report of ${r.name}`} original={frames.original} />
        </Panel>
      )}

      {r.failedCases.length > 0 && (
        <Panel
          id="failed"
          title={
            <>
              Failed tests{" "}
              <span className="text-muted-foreground text-sm font-normal">{r.failedCases.length}</span>
            </>
          }
        >
          <FailedCases cluster={k.cluster} run={r} />
        </Panel>
      )}

      {r.steps.length > 0 && (
        <Panel id="steps" title="Steps">
          <Steps run={r} />
        </Panel>
      )}

      <Panel id="log" title="Log" actions={<LogDownload k={k} />}>
        <RunLog k={k} live={!r.finished} phase={r.phase} />
      </Panel>

      {r.media.length > 0 && (
        <Panel
          id="media"
          title={
            <>
              Screenshots &amp; videos{" "}
              <span className="text-muted-foreground text-sm font-normal">
                {r.moreMedia
                  ? `${r.media.length} of ${r.media.length + r.moreMedia} — the rest are under Artifacts`
                  : r.media.length}
              </span>
            </>
          }
        >
          <Gallery cluster={k.cluster} run={r} />
        </Panel>
      )}

      <Panel id="artifacts" title="Artifacts">
        <Artifacts cluster={k.cluster} run={r} />
      </Panel>

      <Panel id="comment" title="Comment">
        <Comment key={r.comment?.text ?? ""} k={k} run={r} canEdit={canRun} />
      </Panel>

      {canRun && !r.finished && (
        <section className="bg-card rounded-xl border p-5" aria-label="Run actions">
          <AbortRun k={k} />
        </section>
      )}
      {me.data?.can.delete && r.finished && (
        <section className="bg-card rounded-xl border p-5" aria-label="Run actions">
          <DeleteRun k={k} name={r.name} testRef={r.testRef} />
        </section>
      )}

      {(r.events.length > 0 || r.eventsError) && <Events run={r} />}
    </div>
  );
}
