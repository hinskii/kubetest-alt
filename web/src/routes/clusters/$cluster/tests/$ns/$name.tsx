import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { lazy, Suspense, useState, type ReactNode } from "react";
import { z } from "zod";

import { clustersQuery, meQuery, testQuery, testRunsQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { Chip } from "@/components/phase-badge";
import { DeleteTest } from "@/components/test/delete-test";
import { RunForm } from "@/components/test/run-form";
import { RunsTable } from "@/components/test/runs-table";
import { SchedulePanel } from "@/components/test/schedule-panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { goPage } from "@/lib/paths";

// CodeMirror only when the definition is opened.
const YamlView = lazy(() => import("@/components/yaml-view"));

export const Route = createFileRoute("/clusters/$cluster/tests/$ns/$name")({
  validateSearch: z.object({ after: z.string().optional().catch(undefined) }),
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
  const { after } = Route.useSearch();
  const test = useQuery(testQuery(k));
  const runs = useQuery(testRunsQuery(k, after));
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
            <a href={goPage.edit(k.cluster, k.ns, k.name)}>Edit</a>
          </Button>
        )}
        {canRun && (
          <Button variant="outline" asChild title="A GUI-managed copy, opened in the editor">
            <a href={goPage.duplicate(k.cluster, k.ns, k.name)}>Duplicate</a>
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
                  <a href={goPage.edit(k.cluster, k.ns, k.name)} className="underline">
                    edit the Test
                  </a>
                </>
              ) : t.locked ? (
                " — set it where the Test is defined (Git)."
              ) : null}
            </p>
          </AlertDescription>
        </Alert>
      )}

      {canRun && (
        <Panel title="Run" id="run">
          <RunForm k={k} params={t.params} />
        </Panel>
      )}

      {(t.schedule || editable) && (
        <Panel title="Schedule" id="schedule">
          <SchedulePanel key={t.schedule?.expr ?? ""} k={k} schedule={t.schedule} editable={editable} />
          {t.locked && (
            <p className="text-muted-foreground text-sm">
              Managed in Git: change <code className="font-mono">spec.schedule</code> there.
            </p>
          )}
        </Panel>
      )}

      <Panel
        title="Runs"
        id="runs"
        actions={
          <>
            <Button variant="outline" size="sm" asChild>
              <Link to="/clusters/$cluster/tests/$ns/$name/cases" params={k}>
                Test cases &amp; flaky tests
              </Link>
            </Button>
            <Button variant="outline" size="sm" asChild>
              <Link to="/clusters/$cluster/tests/$ns/$name/analytics" params={k}>
                Analytics &amp; comparison
              </Link>
            </Button>
            {canRun && (
              <Button variant="outline" size="sm" asChild>
                <Link to="/clusters/$cluster/tests/$ns/$name/cleanup" params={k}>
                  Clean up runs
                </Link>
              </Button>
            )}
          </>
        }
      >
        {runs.isError && <ErrorView error={runs.error} />}
        {runs.isPending && <Skeleton className="h-24 w-full" />}
        {runs.data &&
          (runs.data.runs.length === 0 ? (
            <p className="text-muted-foreground">No runs yet.</p>
          ) : (
            <>
              <RunsTable cluster={k.cluster} ns={k.ns} runs={runs.data.runs} />
              <nav className="flex gap-4 text-sm" aria-label="Run history pages">
                {after && (
                  <Link from={Route.fullPath} search={{}} className="text-primary hover:underline">
                    Newest
                  </Link>
                )}
                {runs.data.next && (
                  <Link
                    from={Route.fullPath}
                    search={{ after: runs.data.next }}
                    className="text-primary hover:underline"
                  >
                    Older runs
                  </Link>
                )}
              </nav>
            </>
          ))}
      </Panel>

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
    </div>
  );
}
