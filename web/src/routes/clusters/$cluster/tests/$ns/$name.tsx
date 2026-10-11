import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useState, type ReactNode } from "react";
import { z } from "zod";

import { clustersQuery, meQuery, testQuery, testRunsQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { Chip } from "@/components/phase-badge";
import { DeleteTest } from "@/components/test/delete-test";
import { CliPanel } from "@/components/test/cli-panel";
import { LatestRun } from "@/components/test/latest-run";
import { OverviewTiles } from "@/components/test/overview-tiles";
import { RunForm } from "@/components/test/run-form";
import { RunsTable } from "@/components/test/runs-table";
import { TestSettings } from "@/components/test/settings";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { when } from "@/lib/format";

// CodeMirror only when the definition is opened; Recharts with Insights.
const YamlView = lazy(() => import("@/components/yaml-view"));
const InsightsTab = lazy(() => import("@/components/test/insights-tab"));

export const Route = createFileRoute("/clusters/$cluster/tests/$ns/$name")({
  validateSearch: z.object({
    after: z.string().optional().catch(undefined),
    tab: z.enum(["runs", "insights", "cli", "settings"]).optional().catch(undefined),
    range: z.enum(["24h", "7d", "30d"]).optional().catch(undefined),
    phase: z.enum(["queued", "running", "passed", "failed", "error", "aborted"]).optional().catch(undefined),
    q: z.string().optional().catch(undefined),
  }),
  component: TestPage,
});

function Panel({
  title,
  actions,
  children,
  id,
}: {
  title: string;
  actions?: ReactNode;
  children: ReactNode;
  id?: string;
}) {
  return (
    <section
      className="bg-card space-y-4 rounded-xl border p-5"
      aria-labelledby={id && `${id}-title`}
      id={id}
    >
      <div className="flex flex-wrap items-center gap-2">
        <h2 id={id && `${id}-title`} className="mr-auto text-lg font-semibold">
          {title}
        </h2>
        {actions}
      </div>
      {children}
    </section>
  );
}

function TestPage() {
  const k = Route.useParams();
  const { after, tab, range = "7d", phase, q = "" } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const test = useQuery(testQuery(k));
  const latest = useQuery(testRunsQuery(k));
  const runs = useQuery({ ...testRunsQuery(k, after, phase), enabled: tab === "runs" });
  const me = useQuery(meQuery);
  const clusters = useQuery(clustersQuery);
  const [showDefinition, setShowDefinition] = useState(false);
  const clusterTitle = clusters.data?.find((c) => c.name === k.cluster)?.displayName ?? k.cluster;
  const crumbs = [
    { label: "Clusters", to: "/" },
    { label: clusterTitle, to: "/clusters/$cluster", params: { cluster: k.cluster } },
    { label: k.name },
  ];

  if (test.isError) {
    return (
      <>
        <PageHeader crumbs={crumbs} title={k.name} />
        <ErrorView error={test.error} />
      </>
    );
  }
  if (!test.data) return <Skeleton className="h-64 w-full" />;
  const t = test.data;
  const canRun = !!me.data?.can.run;
  const editable = canRun && !t.locked;

  return (
    <div className="space-y-6">
      <PageHeader
        crumbs={crumbs}
        title={
          <span className="flex flex-wrap items-center gap-2">
            {t.name}
            {t.tool && <Chip>{t.tool}</Chip>}
            {t.locked && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Chip tabIndex={0}>read-only</Chip>
                </TooltipTrigger>
                <TooltipContent>Managed outside the GUI: read-only here, runs allowed</TooltipContent>
              </Tooltip>
            )}
          </span>
        }
      >
        {editable && (
          <Button variant="outline" asChild>
            <Link from={Route.fullPath} search={{ tab: "settings" }}>
              Edit
            </Link>
          </Button>
        )}
        {canRun && (
          <Button variant="outline" asChild title="A GUI-managed copy, opened in the editor">
            <Link
              to="/clusters/$cluster/tests/new"
              params={{ cluster: k.cluster }}
              search={{ namespace: k.ns, from: k.name }}
            >
              Duplicate
            </Link>
          </Button>
        )}
        {me.data?.can.delete && !t.locked && <DeleteTest k={k} />}
      </PageHeader>
      <p className="text-muted-foreground -mt-4 text-sm">
        Namespace {t.namespace}
        {t.templates.length > 0 && ` · templates ${t.templates.join(", ")}`}
      </p>

      {t.problem && (
        <Alert className="border-warning/40 bg-warning-soft text-warning" role="status">
          <AlertDescription className="text-warning">
            <p>
              <strong>Not ready to run:</strong> {t.problem}
              {editable ? (
                <>
                  {" "}
                  —{" "}
                  <Link from={Route.fullPath} search={{ tab: "settings" }} className="underline">
                    edit the Test
                  </Link>
                </>
              ) : t.locked ? (
                " — set it where the Test is defined (Git)."
              ) : null}
            </p>
          </AlertDescription>
        </Alert>
      )}

      <Tabs
        value={tab ?? "overview"}
        onValueChange={(v) =>
          void navigate({
            search: (prev) => ({ ...prev, tab: v === "overview" ? undefined : (v as typeof tab) }),
            replace: true,
          })
        }
      >
        <TabsList variant="line" className="h-auto w-full flex-wrap justify-start gap-2 border-b">
          {(
            [
              ["overview", "Overview"],
              ["runs", "Runs"],
              ["insights", "Insights"],
              ["cli", "CLI commands"],
              ["settings", "Settings"],
            ] as const
          ).map(([id, label]) => (
            <TabsTrigger key={id} value={id} className="flex-none px-3">
              {label}
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent value="overview" className="space-y-6 pt-4">
          <OverviewTiles
            k={k}
            range={range}
            onRange={(r) =>
              void navigate({
                search: (prev) => ({ ...prev, range: r === "7d" ? undefined : r }),
                replace: true,
                resetScroll: false,
              })
            }
          />
          {latest.data?.runs[0] && <LatestRun k={k} id={latest.data.runs[0].id} />}
          {latest.data && latest.data.runs.length === 0 && (
            <p className="text-muted-foreground">No runs yet.</p>
          )}
          {canRun && (
            <Panel title="Run" id="run">
              <RunForm k={k} params={t.params} />
            </Panel>
          )}
          {t.schedule && (
            <p className="text-sm" data-testid="schedule-line">
              Runs on a schedule: <code className="font-mono">{t.schedule.expr}</code>
              {t.schedule.next?.[0] && <> · next {when(t.schedule.next[0])}</>}
              {t.schedule.error && <span className="text-destructive"> · {t.schedule.error}</span>} ·{" "}
              <Link
                from={Route.fullPath}
                search={{ tab: "settings" }}
                hash="settings-schedule"
                className="underline"
              >
                {editable ? "change" : "details"}
              </Link>
            </p>
          )}
          <details
            className="bg-card rounded-xl border p-5"
            onToggle={(e) => setShowDefinition((e.target as HTMLDetailsElement).open)}
          >
            <summary className="cursor-pointer font-semibold">
              Definition{" "}
              <span className="text-muted-foreground font-normal">
                (templates merged, expressions not evaluated)
              </span>
            </summary>
            {showDefinition && (
              <div className="mt-4">
                <Suspense fallback={<Skeleton className="h-40 w-full" />}>
                  <YamlView value={t.definition} label="Test definition" />
                </Suspense>
              </div>
            )}
          </details>
        </TabsContent>

        <TabsContent value="runs" className="space-y-6 pt-4">
          <Panel
            title="Runs"
            id="runs"
            actions={
              canRun && (
                <Button variant="outline" size="sm" asChild>
                  <Link to="/clusters/$cluster/tests/$ns/$name/cleanup" params={k}>
                    Clean up runs
                  </Link>
                </Button>
              )
            }
          >
            <div className="flex flex-wrap items-center gap-2">
              <Input
                aria-label="Search runs"
                placeholder="Search by run, who started it, comment"
                className="max-w-sm"
                value={q}
                onChange={(e) =>
                  void navigate({
                    search: (prev) => ({ ...prev, q: e.target.value || undefined }),
                    replace: true,
                    resetScroll: false,
                  })
                }
              />
              <Select
                value={phase ?? "all"}
                onValueChange={(v) =>
                  void navigate({
                    search: (prev) => ({
                      ...prev,
                      phase: v === "all" ? undefined : (v as typeof phase),
                      after: undefined,
                    }),
                    replace: true,
                    resetScroll: false,
                  })
                }
              >
                <SelectTrigger aria-label="Status" className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All statuses</SelectItem>
                  {(["passed", "failed", "error", "aborted", "running", "queued"] as const).map((p) => (
                    <SelectItem key={p} value={p}>
                      {p}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {runs.isError && <ErrorView error={runs.error} />}
            {runs.isPending && <Skeleton className="h-24 w-full" />}
            {runs.data &&
              (() => {
                const needle = q.trim().toLowerCase();
                const shown = needle
                  ? runs.data.runs.filter((r) =>
                      [r.name, r.createdBy, r.comment].some((v) => v?.toLowerCase().includes(needle)),
                    )
                  : runs.data.runs;
                return shown.length === 0 ? (
                  <p className="text-muted-foreground">
                    {runs.data.runs.length === 0
                      ? phase
                        ? `No ${phase} runs.`
                        : "No runs yet."
                      : "No run on this page matches."}
                  </p>
                ) : (
                  <RunsTable cluster={k.cluster} ns={k.ns} runs={shown} />
                );
              })()}
            {runs.data && (after || runs.data.next) && (
              <nav className="flex gap-4 text-sm" aria-label="Run history pages">
                {after && (
                  <Link
                    from={Route.fullPath}
                    search={(prev) => ({ ...prev, after: undefined })}
                    className="text-primary hover:underline"
                  >
                    Newest
                  </Link>
                )}
                {runs.data.next && (
                  <Link
                    from={Route.fullPath}
                    search={(prev) => ({ ...prev, after: runs.data.next })}
                    className="text-primary hover:underline"
                  >
                    Older runs
                  </Link>
                )}
              </nav>
            )}
          </Panel>
        </TabsContent>

        <TabsContent value="insights" className="pt-4">
          <Suspense fallback={<Skeleton className="h-64 w-full" />}>
            <InsightsTab k={k} />
          </Suspense>
        </TabsContent>

        <TabsContent value="cli" className="pt-4">
          <CliPanel
            ns={k.ns}
            name={k.name}
            config={Object.fromEntries(t.params.filter((p) => p.default).map((p) => [p.name, p.default!]))}
          />
        </TabsContent>

        <TabsContent value="settings" className="pt-4">
          <TestSettings k={k} canEdit={canRun} schedule={t.schedule} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
