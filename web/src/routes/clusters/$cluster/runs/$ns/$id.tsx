import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { clustersQuery, meQuery, runQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader, type Crumb } from "@/components/page-header";
import { Chip, PhaseBadge } from "@/components/phase-badge";
import { Comment } from "@/components/run/comment-form";
import { AbortRun, DeleteRun, RunAgain } from "@/components/run/run-actions";
import {
  ChildrenFlow,
  RunFlow,
  WorkerPicker,
  WorkersFlow,
  workerLabel,
  workerName,
  workerSteps,
} from "@/components/run/run-flow";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { LogDownload, logOf, RunLog } from "@/components/run/run-log";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import { artifactUrl } from "@/lib/paths";
import type { RunDetail } from "@/api/client";

export const Route = createFileRoute("/clusters/$cluster/runs/$ns/$id")({
  // ?stage= is the stage whose log is shown (step 20a); default: the one
  // running, else the tool's.
  validateSearch: z.object({
    tab: z
      .enum(["log", "live", "report", "tests", "artifacts", "steps", "details", "events"])
      .optional()
      .catch(undefined),
    stage: z
      .string()
      .regex(/^((worker-\d+\/)?(queue|fetch|run|collect)|worker-\d+)$/)
      .optional()
      .catch(undefined),
  }),
  component: RunPage,
});

type StageKind = string;
type Tab = "log" | "live" | "report" | "tests" | "artifacts" | "steps" | "details" | "events";

function RunPage() {
  const k = Route.useParams();
  const { stage: picked, tab: pickedTab } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const run = useQuery(runQuery(k));
  const me = useQuery(meQuery);
  const clusters = useQuery(clustersQuery);
  const frames = useFrameColors();
  // Live links — the run's (key ""), or each parallel worker's — are
  // signed anew on every refresh: keep the first, or the frame would
  // reload each time.
  const [liveLinks, setLiveLinks] = useState<Record<string, string>>({});
  const offered = liveViewsOf(run.data);
  if (Object.keys(offered).some((key) => !liveLinks[key])) setLiveLinks((l) => ({ ...offered, ...l }));

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
  const activity = !r.finished ? r.events.at(-1) : undefined;
  // The stage whose log is shown: picked, else the one running, else the
  // tool's; a parallel run's: a worker's (the one running, else the
  // first), its running stage, else its tool's.
  const workers = workerSteps(r.steps);
  const worker = workers.find((w) => w.phase === "running") ?? workers[0];
  const stage: StageKind | undefined =
    picked ??
    (r.stages.length > 0
      ? (r.stages.find((s) => s.phase === "running")?.kind ?? "run")
      : worker
        ? worker.stages?.length
          ? `${worker.key}/${worker.stages.find((s) => s.phase === "running")?.kind ?? "run"}`
          : worker.key
        : undefined);
  const [stageWorker, stageKind] = stage?.startsWith("worker-") ? stage.split("/") : [undefined, stage];
  const shownWorker = workers.find((w) => w.key === stageWorker);
  const shownStage = (shownWorker ? (shownWorker.stages ?? []) : r.stages).find((s) => s.kind === stageKind);
  const shownName = [stageWorker && workerName(stageWorker), shownStage?.name].filter(Boolean).join(" · ");
  const shownLog = logOf(stage);
  // A parallel run's tabs show one worker at a time — the one whose log
  // is shown (?stage=worker-N/…), else the first that has what the tab
  // shows. Picking a worker in any tab picks it for all.
  const pickWorker = (key: string) => {
    const kinds = workers.find((w) => w.key === key)?.stages?.map((s) => s.kind);
    go({ stage: kinds?.length ? `${key}/${kinds.find((x) => x === stageKind) ?? "run"}` : key });
  };
  // The live view shown: the run's own, or that worker's.
  const liveKeys = Object.keys(offered);
  const liveKey = [stageWorker, liveKeys[0]].find((key) => key !== undefined && key in offered);
  const liveView = liveKey !== undefined ? liveLinks[liveKey] : undefined;
  // The report shown: the run's, or that worker's (each has its own).
  const reportWorkers = workers.filter((w) => w.report);
  const reportWorker = reportWorkers.find((w) => w.key === stageWorker) ?? reportWorkers[0];
  const reportPath = reportWorker?.report ?? r.report;
  const report = reportPath ? artifactUrl(k.cluster, k.ns, r.id, reportPath) : undefined;

  // Tabs worth showing for this run, with counts; the address keeps the
  // open one (?tab=).
  const tabs: { id: Tab; label: string; count?: number; warn?: boolean }[] = [
    { id: "log", label: "Log" },
    ...(!r.finished && liveView ? [{ id: "live" as Tab, label: "Live view" }] : []),
    ...(r.finished && report ? [{ id: "report" as Tab, label: "Report" }] : []),
    ...(r.failedCases.length > 0 || r.testCounts
      ? [
          {
            id: "tests" as Tab,
            label: "Tests",
            count: r.testCounts?.failed ?? r.failedCases.length,
            warn: true,
          },
        ]
      : []),
    { id: "artifacts", label: "Artifacts", count: r.artifacts.length },
    ...(r.children.length > 0 || r.steps.length > 0
      ? [
          {
            id: "steps" as Tab,
            label: r.children.length > 0 ? "Child runs" : "Steps",
            count: r.children.length || r.steps.length,
          },
        ]
      : []),
    { id: "details", label: "Details" },
    {
      id: "events",
      label: "Events",
      count: r.events.length,
      warn: r.events.some((e) => e.type === "Warning"),
    },
  ];
  const fallback: Tab = r.stages.length === 0 && r.children.length > 0 ? "steps" : "log";
  const tab: Tab = tabs.some((t) => t.id === pickedTab) ? pickedTab! : fallback;
  const go = (search: { tab?: Tab; stage?: StageKind }) =>
    void navigate({ search: (prev) => ({ ...prev, ...search }), replace: true, resetScroll: false });

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
        {reportWorkers.length > 1 ? (
          <OpenWorkerReport
            items={reportWorkers.map((w) => ({
              key: w.key,
              label: workerLabel(w),
              href: artifactUrl(k.cluster, k.ns, r.id, w.report!),
            }))}
          />
        ) : (
          report && (
            <Button asChild>
              <a href={report} target="_blank" rel="noopener noreferrer">
                Open report
              </a>
            </Button>
          )
        )}
        {r.finished && (
          <Button variant="outline" asChild>
            <Link to="/clusters/$cluster/runs/$ns/$id/compare" params={{ ...k, id: r.id }}>
              Compare with another run
            </Link>
          </Button>
        )}
        {canRun && <RunAgain k={k} name={r.name} testRef={r.testRef} />}
        {canRun && !r.finished && <AbortRun k={k} />}
        {me.data?.can.delete && r.finished && <DeleteRun k={k} name={r.name} testRef={r.testRef} />}
      </PageHeader>

      {r.message && <p className="-mt-2">{r.message}</p>}
      {activity && <Activity event={activity} />}

      <RunFlow
        stages={r.stages}
        selected={tab === "log" ? stage : undefined}
        onSelect={(s) => go({ tab: "log", stage: s })}
      />
      <WorkersFlow
        steps={r.steps}
        selected={tab === "log" ? stage : undefined}
        onSelect={(s) => go({ tab: "log", stage: s })}
      />
      <ChildrenFlow cluster={k.cluster} ns={k.ns} items={r.children} />

      <Tabs value={tab} onValueChange={(v) => go({ tab: v as Tab })}>
        <TabsList variant="line" className="h-auto w-full flex-wrap justify-start gap-2 border-b">
          {tabs.map((t) => (
            <TabsTrigger key={t.id} value={t.id} className="flex-none px-3">
              {t.label}
              {t.count ? (
                <span
                  className={cn(
                    "ml-1.5 rounded-full px-1.5 text-xs tabular-nums",
                    t.warn ? "bg-danger-soft text-danger" : "bg-muted text-muted-foreground",
                  )}
                >
                  {t.count}
                </span>
              ) : null}
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent value="log" className="pt-4">
          <Panel id="log" title={shownName ? `Log · ${shownName}` : "Log"} actions={<LogDownload k={k} />}>
            <WorkerPicker workers={workers} selected={stageWorker} onSelect={pickWorker} />
            {shownLog.queue ? (
              <QueueLog run={r} why={shownStage?.message} />
            ) : (
              <RunLog
                key={stage ?? "all"}
                k={k}
                live={!r.finished}
                phase={r.phase}
                stage={shownLog.stage}
                part={shownLog.part}
              />
            )}
          </Panel>
        </TabsContent>

        {!r.finished && liveView && (
          <TabsContent value="live" className="pt-4">
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
                The tool&apos;s own dashboard, live while the run is going. It runs isolated from Control
                Center.
              </p>
              <WorkerPicker
                workers={workers.filter((w) => w.key in offered)}
                selected={liveKey}
                onSelect={pickWorker}
              />
              <ToolFrame
                key={liveKey}
                src={liveView}
                title={`Live view of ${liveKey ? `${r.name}, ${workerName(liveKey).toLowerCase()}` : r.name}`}
                original={frames.original}
              />
            </Panel>
          </TabsContent>
        )}

        {r.finished && report && (
          <TabsContent value="report" className="pt-4">
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
                    <a href={artifactUrl(k.cluster, k.ns, r.id, reportPath!, true)}>Download</a>
                  </Button>
                </>
              }
            >
              <p className="text-muted-foreground text-sm">
                The tool&apos;s report of this run. It runs isolated from Control Center.
              </p>
              <WorkerPicker workers={reportWorkers} selected={reportWorker?.key} onSelect={pickWorker} />
              <ToolFrame
                key={reportPath}
                src={report}
                title={`Report of ${reportWorker ? `${r.name}, ${workerName(reportWorker.key).toLowerCase()}` : r.name}`}
                original={frames.original}
              />
            </Panel>
          </TabsContent>
        )}

        <TabsContent value="tests" className="pt-4">
          <Panel
            id="failed"
            title={
              <>
                Failed tests{" "}
                <span className="text-muted-foreground text-sm font-normal">
                  {r.testCounts
                    ? `${r.failedCases.length} of ${r.testCounts.total} · ${r.testCounts.passed} passed, ${r.testCounts.skipped} skipped`
                    : r.failedCases.length}
                </span>
              </>
            }
          >
            {r.failedCases.length > 0 ? (
              <FailedCases cluster={k.cluster} run={r} />
            ) : (
              <p className="text-muted-foreground">No test failed.</p>
            )}
          </Panel>
        </TabsContent>

        <TabsContent value="artifacts" className="space-y-6 pt-4">
          {r.media.length > 0 && (
            <Panel
              id="media"
              title={
                <>
                  Screenshots &amp; videos{" "}
                  <span className="text-muted-foreground text-sm font-normal">
                    {r.moreMedia
                      ? `${r.media.length} of ${r.media.length + r.moreMedia} — the rest are below`
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
        </TabsContent>

        <TabsContent value="steps" className="space-y-6 pt-4">
          {r.children.length > 0 && (
            <Panel id="children" title="Child runs">
              <Children cluster={k.cluster} run={r} />
            </Panel>
          )}
          {r.steps.length > 0 && (
            <Panel id="steps" title="Steps">
              <Steps run={r} />
            </Panel>
          )}
        </TabsContent>

        <TabsContent value="details" className="space-y-6 pt-4">
          <section className="bg-card rounded-xl border p-5" aria-label="Facts">
            <RunFacts cluster={k.cluster} run={r} />
          </section>
          <Panel id="comment" title="Comment">
            <Comment key={r.comment?.text ?? ""} k={k} run={r} canEdit={canRun} />
          </Panel>
        </TabsContent>

        <TabsContent value="events" className="pt-4">
          <Panel id="events" title="Kubernetes events">
            <Events run={r} />
          </Panel>
        </TabsContent>
      </Tabs>
    </div>
  );
}

/** QueueLog is the queue stage's "log": what Kubernetes said about the pod. */
function QueueLog({ run, why }: { run: RunDetail; why?: string }) {
  const lines = [...(why ? [why, ""] : []), ...run.events.map((e) => `${e.time}  ${e.reason}  ${e.message}`)];
  return (
    <pre
      data-testid="run-log"
      aria-label="Log"
      className="bg-muted max-h-[60vh] overflow-auto rounded-lg border p-3 font-mono text-xs whitespace-pre-wrap"
    >
      {lines.length > 0
        ? lines.join("\n")
        : (run.eventsError ?? "No Kubernetes events: the pod was scheduled and started without a word.")}
    </pre>
  );
}

/** liveViewsOf are a run's live links: its own (key ""), or each running worker's. */
function liveViewsOf(run?: RunDetail): Record<string, string> {
  if (!run || run.finished) return {};
  const out: Record<string, string> = run.liveView ? { "": run.liveView } : {};
  for (const s of run.steps) if (s.liveView) out[s.key] = s.liveView;
  return out;
}

/** OpenWorkerReport opens one of a parallel run's reports: each worker has its own. */
function OpenWorkerReport({ items }: { items: { key: string; label: string; href: string }[] }) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button>Open report</Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-auto p-1">
        <ul aria-label="Reports" className="flex flex-col">
          {items.map((i) => (
            <li key={i.key}>
              <a
                href={i.href}
                target="_blank"
                rel="noopener noreferrer"
                className="hover:bg-muted block rounded-md px-3 py-1.5 text-sm"
              >
                {i.label}
              </a>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  );
}
