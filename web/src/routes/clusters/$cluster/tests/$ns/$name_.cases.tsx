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
  type SortingState,
} from "@tanstack/react-table";
import { ArrowDown, ArrowUp } from "lucide-react";
import { z } from "zod";

import type { CaseStat } from "@/api/client";
import { casesQuery, clustersQuery, toWindow } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { Chip, PhaseBadge } from "@/components/phase-badge";
import { Panel } from "@/components/run/sections";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { duration } from "@/lib/format";

/** Sorts offered, as the address names them; "failures" is the API's order. */
const SORTS = {
  failures: { label: "most failures", sorting: [] as SortingState },
  flaky: { label: "most flips (flaky)", sorting: [{ id: "flips", desc: true }] },
  slowest: { label: "slowest", sorting: [{ id: "avgMs", desc: true }] },
  name: { label: "name", sorting: [{ id: "name", desc: false }] },
} as const;
type Sort = keyof typeof SORTS;
const sortOfColumn: Record<string, Sort> = { flips: "flaky", avgMs: "slowest", name: "name" };

const search = z.object({
  runs: z.coerce.number().optional().catch(undefined),
  sort: z.enum(["failures", "flaky", "slowest", "name"]).optional().catch(undefined),
});

export const Route = createFileRoute("/clusters/$cluster/tests/$ns/$name_/cases")({
  validateSearch: search,
  component: CasesPage,
});

const features = tableFeatures({
  rowSortingFeature,
  sortedRowModel: createSortedRowModel(),
  sortFns: { basic: sortFn_basic, alphanumeric: sortFn_alphanumeric },
});
const col = createColumnHelper<typeof features, CaseStat>();

function passRate(c: CaseStat): string {
  const ran = c.runs - c.skipped;
  return ran > 0 ? `${Math.round((c.passed * 100) / ran)}%` : "—";
}

function CasesPage() {
  const k = Route.useParams();
  const { runs, sort = "failures" } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const clusters = useQuery(clustersQuery);
  const clusterTitle = clusters.data?.find((c) => c.name === k.cluster)?.displayName ?? k.cluster;
  const cases = useQuery(casesQuery(k, toWindow(runs)));

  const columns = col.columns([
    col.accessor("name", {
      header: "Test case",
      sortFn: "alphanumeric",
      cell: (info) => {
        const c = info.row.original;
        return (
          <div>
            <Link
              to="/clusters/$cluster/tests/$ns/$name/cases/history"
              params={k}
              search={{ case: c.key }}
              className="text-primary font-medium hover:underline"
            >
              {c.name}
            </Link>{" "}
            {c.flaky && (
              <Chip tone="warning" title="Passed and failed within this window">
                flaky
              </Chip>
            )}
            {(c.class || c.suite) && (
              <div className="text-muted-foreground text-xs">{c.class || c.suite}</div>
            )}
          </div>
        );
      },
    }),
    col.accessor("lastStatus", {
      header: "Latest",
      enableSorting: false,
      cell: (i) => <PhaseBadge phase={i.getValue()} />,
    }),
    col.display({ id: "rate", header: "Pass rate", cell: (i) => passRate(i.row.original) }),
    col.display({
      id: "counts",
      header: "Passed / failed / skipped",
      cell: (i) => {
        const c = i.row.original;
        return (
          <>
            {c.passed} / {c.failed} / {c.skipped} <span className="text-muted-foreground">of {c.runs}</span>
          </>
        );
      },
    }),
    col.accessor("avgMs", { header: "Avg", sortFn: "basic", cell: (i) => duration(i.getValue()) }),
    col.accessor("maxMs", {
      header: "Max",
      enableSorting: false,
      cell: (i) => <span className="text-muted-foreground">{duration(i.getValue())}</span>,
    }),
    col.accessor("flips", { header: "Flips", sortFn: "basic", cell: (i) => i.getValue() || "" }),
  ]);

  const table = useTable({
    features,
    columns,
    data: cases.data?.cases ?? [],
    state: { sorting: [...SORTS[sort].sorting] },
    onSortingChange: () => {},
  });
  const setSearch = (patch: { runs?: number; sort?: Sort }) =>
    navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });

  return (
    <div className="space-y-6">
      <PageHeader
        crumbs={[
          { label: "Clusters", to: "/" },
          { label: clusterTitle, to: "/clusters/$cluster", params: { cluster: k.cluster } },
          { label: k.name, to: "/clusters/$cluster/tests/$ns/$name", params: k },
          { label: "Test cases" },
        ]}
        title={`${k.name} · test cases`}
      />
      {cases.isError && <ErrorView error={cases.error} />}
      {cases.isPending && <Skeleton className="h-40 w-full" />}
      {cases.data && (
        <>
          <p className="text-muted-foreground -mt-4 text-sm" data-testid="cases-summary">
            {cases.data.cases.length} cases over the last {cases.data.window} runs that reported JUnit ·{" "}
            <strong className="text-foreground">{cases.data.failingNow}</strong> failing in the latest run ·{" "}
            <strong className="text-foreground">{cases.data.flaky}</strong> flaky (passed and failed in this
            window)
          </p>
          <div className="flex flex-wrap items-end gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="window">Window</Label>
              <Select value={String(cases.data.window)} onValueChange={(v) => setSearch({ runs: Number(v) })}>
                <SelectTrigger id="window" className="bg-card w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {cases.data.windows.map((n) => (
                    <SelectItem key={n} value={String(n)}>
                      last {n} runs
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="sort">Sort</Label>
              <Select value={sort} onValueChange={(v) => setSearch({ sort: v as Sort })}>
                <SelectTrigger id="sort" className="bg-card w-48">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(SORTS) as Sort[]).map((s) => (
                    <SelectItem key={s} value={s}>
                      {SORTS[s].label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          {cases.data.cases.length === 0 ? (
            <p className="text-muted-foreground">
              No test cases yet. They appear once a run of this Test that writes JUnit reports has finished.
            </p>
          ) : (
            <Panel id="cases" title="Cases">
              <Table>
                <TableHeader>
                  {table.getHeaderGroups().map((hg) => (
                    <TableRow key={hg.id}>
                      {hg.headers.map((h) => {
                        const target = sortOfColumn[h.column.id];
                        const dir = h.column.getIsSorted();
                        return (
                          <TableHead
                            key={h.id}
                            aria-sort={dir ? (dir === "asc" ? "ascending" : "descending") : undefined}
                          >
                            {target ? (
                              <button
                                type="button"
                                className="inline-flex items-center gap-1 hover:underline"
                                onClick={() => setSearch({ sort: sort === target ? "failures" : target })}
                              >
                                {flexRender(h.column.columnDef.header, h.getContext())}
                                {dir === "asc" && <ArrowUp className="size-3" />}
                                {dir === "desc" && <ArrowDown className="size-3" />}
                              </button>
                            ) : (
                              flexRender(h.column.columnDef.header, h.getContext())
                            )}
                          </TableHead>
                        );
                      })}
                    </TableRow>
                  ))}
                </TableHeader>
                <TableBody>
                  {table.getRowModel().rows.map((row) => (
                    <TableRow key={row.id} data-testid="case-row">
                      {row.getAllCells().map((cell) => (
                        <TableCell key={cell.id}>
                          {flexRender(cell.column.columnDef.cell, cell.getContext())}
                        </TableCell>
                      ))}
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
