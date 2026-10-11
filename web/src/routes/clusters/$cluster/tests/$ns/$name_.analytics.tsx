import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import type { Analytics, Comparison, Trend } from "@/api/client";
import { analyticsQuery, clustersQuery, comparisonQuery, toWindow } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { PhaseBadge } from "@/components/phase-badge";
import { Panel } from "@/components/run/sections";
import { Sparkline } from "@/components/sparkline";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { duration, formatNumber, when } from "@/lib/format";
import { cn } from "@/lib/utils";

/** MaxCompared is the API's limit of runs in one comparison. */
const MAX_COMPARED = 10;

const search = z.object({
  runs: z.coerce.number().optional().catch(undefined),
  run: z.array(z.string()).max(MAX_COMPARED).optional().catch(undefined),
});

export const Route = createFileRoute("/clusters/$cluster/tests/$ns/$name_/analytics")({
  validateSearch: search,
  component: AnalyticsPage,
});

const tick: Record<string, string> = {
  passed: "bg-success",
  failed: "bg-destructive",
  error: "bg-destructive",
  aborted: "bg-unknown",
};

function value(t: Trend, v: number): string {
  return t.unit === "ms" ? duration(Math.round(v)) : formatNumber(v);
}

function AnalyticsPage() {
  const k = Route.useParams();
  const { runs, run: selected = [] } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const clusters = useQuery(clustersQuery);
  const clusterTitle = clusters.data?.find((c) => c.name === k.cluster)?.displayName ?? k.cluster;
  const a = useQuery(analyticsQuery(k, toWindow(runs)));
  const cmp = useQuery({ ...comparisonQuery(k, selected), enabled: selected.length > 0 });

  return (
    <div className="space-y-6">
      <PageHeader
        crumbs={[
          { label: "Clusters", to: "/" },
          { label: clusterTitle, to: "/clusters/$cluster", params: { cluster: k.cluster } },
          { label: k.name, to: "/clusters/$cluster/tests/$ns/$name", params: k },
          { label: "Analytics" },
        ]}
        title={`${k.name} · analytics`}
      />
      {a.isError && <ErrorView error={a.error} />}
      {a.isPending && <Skeleton className="h-40 w-full" />}
      {a.data && (
        <>
          <div className="grid max-w-xs gap-1.5">
            <Label htmlFor="window">Window</Label>
            <Select
              value={String(a.data.window)}
              onValueChange={(v) =>
                navigate({ search: (prev) => ({ ...prev, runs: Number(v) }), replace: true })
              }
            >
              <SelectTrigger id="window" className="bg-card w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {a.data.windows.map((n) => (
                  <SelectItem key={n} value={String(n)}>
                    last {n} runs
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {a.data.runs.length === 0 ? (
            <p className="text-muted-foreground">
              No finished runs yet. Trends and comparisons appear once runs of this Test have finished.
            </p>
          ) : (
            <>
              <Overview k={k} data={a.data} />
              {a.data.trends.length > 0 && <Trends trends={a.data.trends} />}
              {selected.length > 0 && (
                <>
                  {cmp.isError && <ErrorView error={cmp.error} />}
                  {cmp.data && <ComparisonView k={k} c={cmp.data} />}
                </>
              )}
              <Picker
                k={k}
                runs={a.data.runs}
                selected={selected}
                onCompare={(run) => navigate({ search: (prev) => ({ ...prev, run }) })}
              />
            </>
          )}
        </>
      )}
    </div>
  );
}

type K = { cluster: string; ns: string; name: string };
type A = Analytics;

function Overview({ k, data }: { k: K; data: A }) {
  const s = data.summary;
  const strip = [...data.runs].reverse();
  return (
    <Panel
      id="overview"
      title={
        <>
          Overview{" "}
          <span className="text-muted-foreground text-sm font-normal">{s.finished} finished runs</span>
        </>
      }
    >
      <div className="flex flex-wrap gap-1" aria-label="Run phases, oldest first">
        {strip.map((r) => (
          <Link
            key={r.id}
            to="/clusters/$cluster/runs/$ns/$id"
            params={{ cluster: k.cluster, ns: k.ns, id: r.id }}
            title={`${r.name} · ${r.phase}${r.startedAt ? ` · ${when(r.startedAt)}` : ""}`}
            aria-label={`${r.name}: ${r.phase}`}
            className={cn("h-6 w-2 rounded-sm", tick[r.phase] ?? "bg-unknown")}
          />
        ))}
      </div>
      <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label="Pass rate" value={s.passRate || "—"} />
        <Stat
          label="Passed · failed · error · aborted"
          value={`${s.passed} · ${s.failed} · ${s.errored} · ${s.aborted}`}
        />
        <Stat
          label="Duration avg · p95"
          value={s.avgMs ? `${duration(s.avgMs)} · ${duration(s.p95Ms)}` : "—"}
        />
        {s.lastFailure && (
          <Stat
            label="Last failure"
            value={
              <Link
                to="/clusters/$cluster/runs/$ns/$id"
                params={{ cluster: k.cluster, ns: k.ns, id: s.lastFailure.id }}
                className="text-primary hover:underline"
              >
                {s.lastFailure.name}
              </Link>
            }
          />
        )}
        {data.cases && (
          <Stat
            label="Test cases"
            value={
              data.cases.error ? (
                <span className="text-muted-foreground">unavailable: {data.cases.error}</span>
              ) : (
                <Link
                  to="/clusters/$cluster/tests/$ns/$name/cases"
                  params={k}
                  search={{ runs: data.window, sort: "flaky" }}
                  className="text-primary hover:underline"
                >
                  {data.cases.flaky} flaky · {data.cases.failingNow} failing now
                </Link>
              )
            }
          />
        )}
      </dl>
    </Panel>
  );
}

function Stat({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div>
      <dt className="text-muted-foreground text-sm">{label}</dt>
      <dd className="text-lg font-semibold">{value}</dd>
    </div>
  );
}

function Trends({ trends }: { trends: Trend[] }) {
  return (
    <Panel
      id="trends"
      title={
        <>
          Trends{" "}
          <span className="text-muted-foreground text-sm font-normal">
            passed and failed runs, oldest → newest
          </span>
        </>
      }
    >
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Value</TableHead>
            <TableHead>Trend</TableHead>
            <TableHead>Latest</TableHead>
            <TableHead>vs earlier avg</TableHead>
            <TableHead>Min</TableHead>
            <TableHead>Avg</TableHead>
            <TableHead>Max</TableHead>
            <TableHead>Runs</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {trends.map((t) => (
            <TableRow key={t.label} data-testid={`trend-${t.label}`}>
              <TableCell className="font-mono">{t.label}</TableCell>
              <TableCell>
                <Sparkline values={t.values} label={t.label} />
              </TableCell>
              <TableCell className="font-semibold">{value(t, t.latest)}</TableCell>
              <TableCell className="text-muted-foreground">{t.delta}</TableCell>
              <TableCell className="text-muted-foreground">{value(t, t.min)}</TableCell>
              <TableCell className="text-muted-foreground">{value(t, t.avg)}</TableCell>
              <TableCell className="text-muted-foreground">{value(t, t.max)}</TableCell>
              <TableCell className="text-muted-foreground">{t.values.length}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Panel>
  );
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/markdown;charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

function ComparisonView({ k, c }: { k: K; c: Comparison }) {
  const cols = c.runs.length + 1;
  const section = (title: string, rows: Comparison["metrics"]) =>
    rows.length > 0 && (
      <>
        <TableRow>
          <TableHead colSpan={cols} scope="rowgroup" className="bg-muted">
            {title}
          </TableHead>
        </TableRow>
        {rows.map((r) => (
          <TableRow key={`${title}-${r.label}`}>
            <TableHead scope="row" className="font-mono">
              {r.label}
            </TableHead>
            {r.cells.map((cell, i) => (
              <TableCell key={i}>
                {cell.value || <span className="text-muted-foreground">—</span>}
                {cell.delta && <span className="text-muted-foreground"> ({cell.delta})</span>}
              </TableCell>
            ))}
          </TableRow>
        ))}
      </>
    );
  return (
    <Panel
      id="comparison"
      title="Comparison"
      actions={
        <Button variant="outline" size="sm" onClick={() => download(`${k.name}-comparison.md`, c.markdown)}>
          Download Markdown
        </Button>
      }
    >
      {c.missing.length > 0 && (
        <p className="text-muted-foreground text-sm">
          Not runs of this Test (skipped): {c.missing.join(", ")}
        </p>
      )}
      {c.runs.length > 0 && (
        <>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead />
                  {c.runs.map((r) => (
                    <TableHead key={r.id}>
                      <Link
                        to="/clusters/$cluster/runs/$ns/$id"
                        params={{ cluster: k.cluster, ns: k.ns, id: r.id }}
                        className="text-primary hover:underline"
                      >
                        {r.name}
                      </Link>
                    </TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {c.facts.map((r) => (
                  <TableRow key={`fact-${r.label}`}>
                    <TableHead scope="row">{r.label}</TableHead>
                    {r.cells.map((cell, i) => (
                      <TableCell key={i}>{cell.value}</TableCell>
                    ))}
                  </TableRow>
                ))}
                {section("Metrics", c.metrics)}
                {section("Parameters", c.params)}
              </TableBody>
            </Table>
          </div>
          {c.metrics.length > 0 && c.runs.length > 1 && (
            <p className="text-muted-foreground text-xs">
              Changes in brackets are against the first (oldest) run.
            </p>
          )}
          <details>
            <summary className="cursor-pointer text-sm">Markdown</summary>
            <textarea
              readOnly
              rows={12}
              aria-label="Comparison as Markdown"
              value={c.markdown}
              className="bg-muted mt-2 w-full rounded border p-2 font-mono text-xs"
            />
          </details>
        </>
      )}
    </Panel>
  );
}

function Picker({
  k,
  runs,
  selected,
  onCompare,
}: {
  k: K;
  runs: A["runs"];
  selected: string[];
  onCompare: (run: string[]) => void;
}) {
  const [picked, setPicked] = useState<string[]>(selected);
  const toggle = (id: string, on: boolean) =>
    setPicked((p) => (on ? (p.includes(id) ? p : [...p, id]) : p.filter((x) => x !== id)));
  return (
    <Panel id="pick" title="Compare runs">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-10" />
            <TableHead>Run</TableHead>
            <TableHead>Phase</TableHead>
            <TableHead>Started</TableHead>
            <TableHead>Duration</TableHead>
            <TableHead>Note</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {runs.map((r) => (
            <TableRow key={r.id}>
              <TableCell>
                <Checkbox
                  aria-label={`compare ${r.name}`}
                  checked={picked.includes(r.id)}
                  disabled={!picked.includes(r.id) && picked.length >= MAX_COMPARED}
                  onCheckedChange={(v) => toggle(r.id, v === true)}
                />
              </TableCell>
              <TableCell>
                <Link
                  to="/clusters/$cluster/runs/$ns/$id"
                  params={{ cluster: k.cluster, ns: k.ns, id: r.id }}
                  className="text-primary hover:underline"
                >
                  {r.name}
                </Link>
              </TableCell>
              <TableCell>
                <PhaseBadge phase={r.phase} />
              </TableCell>
              <TableCell className="text-muted-foreground">{when(r.startedAt)}</TableCell>
              <TableCell>{duration(r.durationMs)}</TableCell>
              <TableCell className="max-w-xs truncate">{r.comment}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <div className="flex items-center gap-2">
        <Button disabled={picked.length === 0} onClick={() => onCompare(picked)}>
          Compare selected
        </Button>
        <span className="text-muted-foreground text-xs">up to {MAX_COMPARED} runs</span>
      </div>
    </Panel>
  );
}
