import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
  createColumnHelper,
  createSortedRowModel,
  flexRender,
  rowSortingFeature,
  sortFn_alphanumeric,
  sortFn_basic,
  tableFeatures,
  useTable,
} from "@tanstack/react-table";
import { ArrowDown, ArrowUp, ArrowUpDown, Search } from "lucide-react";
import { z } from "zod";

import type { TestSummary } from "@/api/client";
import { clustersQuery, meQuery, testsQuery } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { Chip, PhaseBadge } from "@/components/phase-badge";
import { RecentRuns } from "@/components/test/recent-runs";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { duration, when } from "@/lib/format";

/** Columns the list sorts by, as the address names them. */
const SORTS = ["name", "passRate", "p50", "lastRun"] as const;
type Sort = (typeof SORTS)[number];

const search = z.object({
  namespace: z.string().optional().catch(undefined),
  tool: z.string().optional().catch(undefined),
  /** q filters by name as you type. */
  q: z.string().optional().catch(undefined),
  /** status is the last run's: passed, failed (failed, error, aborted), or never run. */
  status: z.enum(["passed", "failed", "never"]).optional().catch(undefined),
  sort: z.enum(SORTS).optional().catch(undefined),
  desc: z.boolean().optional().catch(undefined),
});
type Search = z.infer<typeof search>;

export const Route = createFileRoute("/clusters/$cluster/")({
  validateSearch: search,
  component: TestsPage,
});

/** toolOther groups Tests without a tool label. */
const toolOther = "other";
const ALL = "__all";

const toolOf = (t: TestSummary) => t.tool || toolOther;

const FAILED = new Set(["failed", "error", "aborted"]);

/** statusOf is the last run's status as the filter names it. */
function statusOf(t: TestSummary): Search["status"] | undefined {
  if (!t.latestRun) return "never";
  if (t.latestRun.phase === "passed") return "passed";
  return FAILED.has(t.latestRun.phase) ? "failed" : undefined;
}

const STATUS_LABELS = { passed: "Last run passed", failed: "Last run failed", never: "Never run" } as const;

function TestsPage() {
  const { cluster } = Route.useParams();
  const { namespace, tool, q, status, sort, desc } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const tests = useQuery(testsQuery(cluster));
  const me = useQuery(meQuery);
  const clusters = useQuery(clustersQuery);
  const title = clusters.data?.find((c) => c.name === cluster)?.displayName ?? cluster;

  const all = tests.data?.tests ?? [];
  const namespaces = [...new Set(all.map((t) => t.namespace))].sort();
  const tools = [...new Set(all.map(toolOf))].sort();
  const needle = q?.trim().toLowerCase();
  const shown = all.filter(
    (t) =>
      (!namespace || t.namespace === namespace) &&
      (!tool || toolOf(t) === tool) &&
      (!status || statusOf(t) === status) &&
      (!needle || t.name.toLowerCase().includes(needle)),
  );
  const groups = Map.groupBy(shown, toolOf);

  const setSearch = (patch: Partial<Search>, replace = false) =>
    navigate({ search: (prev) => ({ ...prev, ...patch }), replace });
  const setFilter = (key: "namespace" | "tool" | "status", value: string) =>
    setSearch({ [key]: value === ALL ? undefined : value });
  // A column's header: ascending, then descending, then back to by name.
  const onSort = (s: Sort) =>
    setSearch(
      sort !== s
        ? { sort: s, desc: undefined }
        : !desc
          ? { desc: true }
          : { sort: undefined, desc: undefined },
      true,
    );

  return (
    <>
      <PageHeader crumbs={[{ label: "Clusters", to: "/" }, { label: title }]} title={title}>
        <Button variant="outline" asChild>
          <Link to="/clusters/$cluster/schedules" params={{ cluster }}>
            Schedules
          </Link>
        </Button>
        {me.data?.can.run && (
          <Button asChild>
            <Link to="/clusters/$cluster/tests/new" params={{ cluster }} search={{ namespace }}>
              New test
            </Link>
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
            <Filter
              label="Status"
              value={status}
              options={Object.keys(STATUS_LABELS)}
              labels={STATUS_LABELS}
              onChange={(v) => setFilter("status", v)}
            />
            <div className="grid gap-1.5">
              <Label htmlFor="filter-search">Search</Label>
              <div className="relative">
                <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                <Input
                  id="filter-search"
                  type="search"
                  placeholder="Test name"
                  className="bg-card w-56 pl-8"
                  value={q ?? ""}
                  onChange={(e) => setSearch({ q: e.target.value || undefined }, true)}
                />
              </div>
            </div>
            <p className="text-muted-foreground pb-2 text-sm" data-testid="tests-count">
              {shown.length} of {all.length} tests
            </p>
          </div>
          {tests.data.statsUnavailable && (
            <p className="text-muted-foreground text-sm">
              No pass rates or recent runs: {tests.data.statsUnavailable}.
            </p>
          )}
          {shown.length === 0 && <p>No tests match.</p>}
          {[...groups.keys()].sort().map((g) => (
            <section key={g} className="space-y-2" data-testid={`group-${g}`}>
              <h2 className="flex items-center gap-2 text-lg font-semibold">
                <Chip>{g}</Chip>
                <span className="text-muted-foreground text-sm font-normal">{groups.get(g)!.length}</span>
              </h2>
              <TestsTable
                cluster={cluster}
                tests={groups.get(g)!}
                range={tests.data.range}
                sort={sort}
                desc={desc}
                onSort={onSort}
              />
            </section>
          ))}
        </div>
      )}
    </>
  );
}

function Filter(props: {
  label: string;
  value?: string;
  options: string[];
  labels?: Record<string, string>;
  onChange: (v: string) => void;
}) {
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
              {props.labels?.[o] ?? o}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

const features = tableFeatures({
  rowSortingFeature,
  sortedRowModel: createSortedRowModel(),
  sortFns: { basic: sortFn_basic, alphanumeric: sortFn_alphanumeric },
});
const col = createColumnHelper<typeof features, TestSummary>();

/** pct renders a 0..1 ratio as "85%". */
const pct = (r: number) => `${Math.round(r * 100)}%`;

function TestsTable({
  cluster,
  tests,
  range,
  sort,
  desc,
  onSort,
}: {
  cluster: string;
  tests: TestSummary[];
  range: string;
  sort?: Sort;
  desc?: boolean;
  onSort: (s: Sort) => void;
}) {
  const columns = col.columns([
    col.accessor("name", {
      id: "name",
      header: "Test",
      sortFn: "alphanumeric",
      cell: (i) => (
        <Link
          to="/clusters/$cluster/tests/$ns/$name"
          params={{ cluster, ns: i.row.original.namespace, name: i.getValue() }}
          className="text-primary font-medium hover:underline"
        >
          {i.getValue()}
        </Link>
      ),
    }),
    col.accessor("namespace", {
      header: "Namespace",
      enableSorting: false,
      cell: (i) => <span className="text-muted-foreground">{i.getValue()}</span>,
    }),
    col.accessor((t) => t.stats?.passRatio ?? undefined, {
      id: "passRate",
      header: `Pass rate (${range})`,
      sortFn: "basic",
      sortUndefined: "last",
      cell: (i) => {
        const st = i.row.original.stats;
        const ratio = i.getValue();
        if (ratio === undefined) return <span className="text-muted-foreground">—</span>;
        const bad = ratio < 0.8;
        return (
          <span className="flex items-baseline gap-1.5">
            <span className={bad ? "text-danger font-medium" : "font-medium"}>{pct(ratio)}</span>
            <span className="text-muted-foreground text-xs">of {st!.total}</span>
          </span>
        );
      },
    }),
    col.accessor((t) => t.stats?.p50Ms || undefined, {
      id: "p50",
      header: "P50",
      sortFn: "basic",
      sortUndefined: "last",
      cell: (i) => duration(i.getValue()) || <span className="text-muted-foreground">—</span>,
    }),
    col.display({
      id: "recent",
      header: "Recent",
      cell: (i) => (
        <RecentRuns
          cluster={cluster}
          ns={i.row.original.namespace}
          runs={i.row.original.stats?.recent ?? []}
        />
      ),
    }),
    col.accessor((t) => (t.latestRun?.finishedAt ? Date.parse(t.latestRun.finishedAt) : undefined), {
      id: "lastRun",
      header: "Last run",
      sortFn: "basic",
      sortUndefined: "last",
      cell: (i) => {
        const t = i.row.original;
        return t.latestRun ? (
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
        );
      },
    }),
    col.display({
      id: "state",
      header: () => <span className="sr-only">State</span>,
      cell: (i) => {
        const t = i.row.original;
        return (
          <span className="flex flex-wrap justify-end gap-1">
            {t.problem && <Hint label="not ready" tone="warning" text={t.problem} />}
            {t.locked && (
              <Hint
                label="read-only"
                text="Managed outside the GUI (GitOps or kubectl): read-only here, runs allowed"
              />
            )}
          </span>
        );
      },
    }),
  ]);
  const table = useTable({
    features,
    columns,
    data: tests,
    // By name unless a column is picked; namespace breaks ties (the API's order).
    state: { sorting: sort ? [{ id: sort, desc: !!desc }] : [] },
    onSortingChange: () => {},
  });
  const widths: Record<string, string> = {
    name: "w-[22%]",
    namespace: "w-[12%]",
    passRate: "w-[13%]",
    p50: "w-[9%]",
    recent: "w-[14%]",
    lastRun: "w-[20%]",
    state: "w-[10%]",
  };

  return (
    <div className="bg-card overflow-x-auto rounded-xl border">
      <Table className="min-w-[56rem] table-fixed">
        <TableHeader>
          {table.getHeaderGroups().map((hg) => (
            <TableRow key={hg.id}>
              {hg.headers.map((h) => {
                const dir = h.column.getIsSorted();
                const header = flexRender(h.column.columnDef.header, h.getContext());
                return (
                  <TableHead
                    key={h.id}
                    className={widths[h.column.id]}
                    aria-sort={dir ? (dir === "asc" ? "ascending" : "descending") : undefined}
                  >
                    {h.column.getCanSort() ? (
                      <button
                        type="button"
                        className="inline-flex items-center gap-1 hover:underline"
                        onClick={() => onSort(h.column.id as Sort)}
                      >
                        {header}
                        {dir === "asc" && <ArrowUp className="size-3" />}
                        {dir === "desc" && <ArrowDown className="size-3" />}
                        {!dir && <ArrowUpDown aria-hidden className="text-muted-foreground/60 size-3" />}
                      </button>
                    ) : (
                      header
                    )}
                  </TableHead>
                );
              })}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {table.getRowModel().rows.map((row) => (
            <TableRow key={`${row.original.namespace}/${row.original.name}`} data-testid="test-row">
              {row.getAllCells().map((cell) => (
                <TableCell key={cell.id} className="truncate">
                  {flexRender(cell.column.columnDef.cell, cell.getContext())}
                </TableCell>
              ))}
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
