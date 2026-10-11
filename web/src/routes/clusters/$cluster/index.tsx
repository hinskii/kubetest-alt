import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { z } from "zod";

import type { TestSummary } from "@/api/client";
import { clustersQuery, meQuery, testsQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { Chip, PhaseBadge } from "@/components/phase-badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { when } from "@/lib/format";
import { goPage } from "@/lib/paths";

const search = z.object({
  namespace: z.string().optional().catch(undefined),
  tool: z.string().optional().catch(undefined),
});

export const Route = createFileRoute("/clusters/$cluster/")({
  validateSearch: search,
  component: TestsPage,
});

/** toolOther groups Tests without a tool label. */
const toolOther = "other";
const ALL = "__all";

const toolOf = (t: TestSummary) => t.tool || toolOther;

function TestsPage() {
  const { cluster } = Route.useParams();
  const { namespace, tool } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const tests = useQuery(testsQuery(cluster));
  const me = useQuery(meQuery);
  const clusters = useQuery(clustersQuery);
  const title = clusters.data?.find((c) => c.name === cluster)?.displayName ?? cluster;

  const all = tests.data?.tests ?? [];
  const namespaces = [...new Set(all.map((t) => t.namespace))].sort();
  const tools = [...new Set(all.map(toolOf))].sort();
  const shown = all.filter((t) => (!namespace || t.namespace === namespace) && (!tool || toolOf(t) === tool));
  const groups = Map.groupBy(shown, toolOf);

  const setFilter = (key: "namespace" | "tool", value: string) =>
    navigate({ search: (prev) => ({ ...prev, [key]: value === ALL ? undefined : value }) });

  return (
    <>
      <PageHeader crumbs={[{ label: "Clusters", to: "/" }, { label: title }]} title={title}>
        <Button variant="outline" asChild>
          <a href={goPage.schedules(cluster)}>Schedules</a>
        </Button>
        {me.data?.can.run && (
          <Button asChild>
            <a href={goPage.newTest(cluster, namespace)}>New test</a>
          </Button>
        )}
      </PageHeader>

      {tests.isError && <ErrorView error={tests.error} />}
      {tests.isPending && <Skeleton className="h-40 w-full" />}
      {tests.data && (
        <div className="space-y-6">
          <div className="flex flex-wrap items-end gap-4">
            <Filter
              label="Namespace"
              value={namespace}
              options={namespaces}
              onChange={(v) => setFilter("namespace", v)}
            />
            <Filter label="Tool" value={tool} options={tools} onChange={(v) => setFilter("tool", v)} />
            <p className="text-muted-foreground pb-2 text-sm" data-testid="tests-count">
              {shown.length} of {all.length} tests
            </p>
          </div>
          {shown.length === 0 && <p>No tests match.</p>}
          {[...groups.keys()].sort().map((g) => (
            <section key={g} className="space-y-2" data-testid={`group-${g}`}>
              <h2 className="flex items-center gap-2 text-lg font-semibold">
                <Chip>{g}</Chip>
                <span className="text-muted-foreground text-sm font-normal">{groups.get(g)!.length}</span>
              </h2>
              <TestsTable cluster={cluster} tests={groups.get(g)!} />
            </section>
          ))}
        </div>
      )}
    </>
  );
}

function Filter(props: { label: string; value?: string; options: string[]; onChange: (v: string) => void }) {
  const id = `filter-${props.label.toLowerCase()}`;
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{props.label}</Label>
      <Select value={props.value ?? ALL} onValueChange={props.onChange}>
        <SelectTrigger id={id} className="w-48 bg-card">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>All</SelectItem>
          {props.options.map((o) => (
            <SelectItem key={o} value={o}>
              {o}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function TestsTable({ cluster, tests }: { cluster: string; tests: TestSummary[] }) {
  return (
    <div className="bg-card rounded-xl border">
      <Table className="table-fixed">
        <TableHeader>
          <TableRow>
            <TableHead className="w-[32%]">Test</TableHead>
            <TableHead className="w-[18%]">Namespace</TableHead>
            <TableHead className="w-[32%]">Last run</TableHead>
            <TableHead className="w-[18%]">
              <span className="sr-only">State</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {tests.map((t) => (
            <TableRow key={`${t.namespace}/${t.name}`}>
              <TableCell className="truncate">
                <Link
                  to="/clusters/$cluster/tests/$ns/$name"
                  params={{ cluster, ns: t.namespace, name: t.name }}
                  className="text-primary font-medium hover:underline"
                >
                  {t.name}
                </Link>
              </TableCell>
              <TableCell className="text-muted-foreground">{t.namespace}</TableCell>
              <TableCell>
                {t.latestRun ? (
                  <span className="flex items-center gap-2">
                    <Link
                      to="/clusters/$cluster/runs/$ns/$id"
                      params={{ cluster, ns: t.namespace, id: t.latestRun.name }}
                      aria-label={`Latest run: ${t.latestRun.phase}`}
                    >
                      <PhaseBadge phase={t.latestRun.phase} />
                    </Link>
                    <span className="text-muted-foreground text-xs">{when(t.latestRun.finishedAt)}</span>
                  </span>
                ) : (
                  <span className="text-muted-foreground">never run</span>
                )}
              </TableCell>
              <TableCell className="space-x-1 text-right">
                {t.problem && <Hint label="not ready" tone="warning" text={t.problem} />}
                {t.locked && (
                  <Hint
                    label="read-only"
                    text="Managed outside the GUI (GitOps or kubectl): read-only here, runs allowed"
                  />
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function Hint({ label, text, tone }: { label: string; text: string; tone?: "warning" }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Chip tone={tone} tabIndex={0} aria-label={`${label}: ${text}`}>
          {label}
        </Chip>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{text}</TooltipContent>
    </Tooltip>
  );
}
