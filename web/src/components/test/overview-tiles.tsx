import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import type { TestOverview } from "@/api/client";
import { testOverviewQuery, type TestKey } from "@/api/queries";
import { Skeleton } from "@/components/ui/skeleton";
import { duration } from "@/lib/format";
import { cn } from "@/lib/utils";

export type Range = "24h" | "7d" | "30d";
const ranges: { id: Range; label: string }[] = [
  { id: "24h", label: "Last 24 hours" },
  { id: "7d", label: "Last week" },
  { id: "30d", label: "Last 30 days" },
];

/** chartY is a value's height in the 100×40 chart box (top 2, bottom 38). */
function chartY(v: number, top: number): number {
  return top > 0 ? 40 - (v / top) * 34 - 2 : 38;
}

/** chartX is point i of n across the 100-wide box. */
function chartX(i: number, n: number): number {
  return n === 1 ? 50 : (i / (n - 1)) * 100;
}

/**
 * areaPath draws values (oldest → newest) as a filled area in a 100×40
 * box; the top line too. Hand-made SVG: no chart library on this page.
 */
export function areaPath(values: number[], max?: number): { area: string; line: string } {
  if (values.length === 0) return { area: "", line: "" };
  const top = max ?? Math.max(...values, 0);
  const pts = values.map((v, i) => `${chartX(i, values.length).toFixed(2)},${chartY(v, top).toFixed(2)}`);
  const line = `M${pts.join(" L")}`;
  return { area: `${line} L100,40 L0,40 Z`, line };
}

/** A chart point: its value, and how it reads (for screen readers). */
export type Point = { value: number; tip: string };

function Tile({
  label,
  value,
  points,
  max,
  tone,
  hint,
  at,
  period,
  onAt,
}: {
  label: string;
  value: string;
  points: Point[];
  max?: number;
  tone: "success" | "danger" | "primary" | "muted";
  hint?: string;
  /** at is the picked bucket, shared by every tile; onAt picks one. */
  at?: number;
  /** period names the picked bucket, beside the value (while hovering). */
  period?: string;
  onAt: (i: number) => void;
}) {
  const values = points.map((p) => p.value);
  const { area, line } = areaPath(values, max);
  const top = max ?? Math.max(...values, 0);
  const color = {
    success: "text-success",
    danger: "text-danger",
    primary: "text-primary",
    muted: "text-muted-foreground",
  }[tone];
  const n = points.length;
  const pick = (clientX: number, el: HTMLElement) => {
    const r = el.getBoundingClientRect();
    if (r.width === 0) return;
    const f = Math.min(1, Math.max(0, (clientX - r.left) / r.width));
    onAt(n === 1 ? 0 : Math.round(f * (n - 1)));
  };
  const shown = at !== undefined ? points[at] : undefined;
  const x = at !== undefined ? chartX(at, n) : 0;
  return (
    <div className="bg-card flex flex-col rounded-xl border" data-testid="overview-tile">
      <div className="p-4 pb-2">
        <div className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{label}</div>
        <div className="mt-1 flex items-baseline gap-2">
          <span className="text-3xl font-semibold tabular-nums" data-testid="tile-value">
            {value}
          </span>
          {period && (
            <span className="text-muted-foreground text-sm" data-testid="tile-period">
              {period}
            </span>
          )}
        </div>
        <div className="text-muted-foreground mt-1 min-h-4 text-xs">{hint}</div>
      </div>
      {area && (
        <div
          role="slider"
          aria-label={`${label} over time`}
          aria-valuemin={0}
          aria-valuemax={n - 1}
          aria-valuenow={at ?? n - 1}
          aria-valuetext={(shown ?? points[n - 1])?.tip}
          tabIndex={0}
          className={cn("relative mt-auto h-14 w-full cursor-crosshair rounded-b-xl outline-none", color)}
          onMouseMove={(e) => pick(e.clientX, e.currentTarget)}
          onFocus={() => onAt(at ?? n - 1)}
          onKeyDown={(e) => {
            if (e.key === "ArrowLeft") onAt(Math.max(0, (at ?? n - 1) - 1));
            if (e.key === "ArrowRight") onAt(Math.min(n - 1, (at ?? 0) + 1));
          }}
        >
          <svg
            aria-hidden
            viewBox="0 0 100 40"
            preserveAspectRatio="none"
            className="block h-full w-full overflow-hidden rounded-b-xl"
          >
            <path d={area} fill="currentColor" opacity={0.15} />
            <path
              d={line}
              fill="none"
              stroke="currentColor"
              strokeWidth={1.5}
              vectorEffect="non-scaling-stroke"
            />
          </svg>
          {shown && (
            <>
              <span
                aria-hidden
                className="pointer-events-none absolute inset-y-0 w-px bg-current opacity-50"
                style={{ left: `${x}%` }}
              />
              <span
                aria-hidden
                className="bg-card pointer-events-none absolute size-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-current"
                style={{ left: `${x}%`, top: `${(chartY(shown.value, top) / 40) * 100}%` }}
              />
            </>
          )}
        </div>
      )}
    </div>
  );
}

/** bucketLabel names a bucket: its hour (24h) or day, in UTC. */
export function bucketLabel(start: string, range: Range): string {
  const d = new Date(start);
  const day = d.toLocaleDateString("en", { month: "short", day: "numeric", timeZone: "UTC" });
  if (range !== "24h") return day;
  return `${day} ${String(d.getUTCHours()).padStart(2, "0")}:00 UTC`;
}

/** passRatios are each bucket's pass ratio (0–100), carried over empty buckets. */
function passRatios(o: TestOverview): number[] {
  let last = 0;
  return o.buckets.map((b) => {
    const verdicts = b.passed + b.failed;
    if (verdicts > 0) last = (b.passed * 100) / verdicts;
    return last;
  });
}

/**
 * OverviewTiles: a Test's runs over the range (?range=) — pass rate,
 * failed, total, average duration — each over its trend (step 20c).
 */
export function OverviewTiles({
  k,
  range,
  onRange,
}: {
  k: TestKey;
  range: Range;
  onRange: (r: Range) => void;
}) {
  const q = useQuery(testOverviewQuery(k, range));
  const o = q.data;
  const s = o?.summary;
  // The bucket picked on any chart: every tile shows that hour or day,
  // until the pointer leaves the tiles.
  const [at, setAt] = useState<number>();
  const b = at !== undefined ? o?.buckets[at] : undefined;
  const pct = (passed: number, failed: number) =>
    passed + failed > 0 ? `${Math.round((passed * 100) / (passed + failed))}%` : "—";
  return (
    <section aria-label="Insights for the range" className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-muted-foreground mr-auto text-xs font-medium tracking-wide uppercase">
          Test runs · {ranges.find((r) => r.id === range)?.label.toLowerCase()}
        </h2>
        <div role="group" aria-label="Range" className="flex rounded-lg border p-0.5">
          {ranges.map((r) => (
            <button
              key={r.id}
              type="button"
              aria-pressed={r.id === range}
              onClick={() => onRange(r.id)}
              className={cn(
                "rounded-md px-2.5 py-1 text-sm",
                r.id === range
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {r.id}
            </button>
          ))}
        </div>
      </div>
      {q.isPending ? (
        <Skeleton className="h-32 w-full" />
      ) : !o || !s ? null : (
        <div
          className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4"
          onMouseLeave={() => setAt(undefined)}
          onBlur={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget)) setAt(undefined);
          }}
        >
          <Tile
            label="Pass rate"
            value={b ? pct(b.passed, b.failed) : s.passRate || "—"}
            hint={
              b
                ? b.passed + b.failed > 0
                  ? `${b.passed} of ${b.passed + b.failed} passed`
                  : "no runs"
                : undefined
            }
            points={o.buckets.map((x, i) => ({
              value: passRatios(o)[i]!,
              tip: `${bucketLabel(x.start, range)}: ${pct(x.passed, x.failed)} passed`,
            }))}
            max={100}
            tone="success"
            at={at}
            period={b && bucketLabel(b.start, range)}
            onAt={setAt}
          />
          <Tile
            label="Failed runs"
            value={String(b ? b.failed : s.failed + s.errored)}
            hint={
              b ? (b.total > 0 ? `of ${b.total} ${b.total === 1 ? "run" : "runs"}` : "no runs") : undefined
            }
            points={o.buckets.map((x) => ({
              value: x.failed,
              tip: `${bucketLabel(x.start, range)}: ${x.failed} failed of ${x.total}`,
            }))}
            tone="danger"
            at={at}
            period={b && bucketLabel(b.start, range)}
            onAt={setAt}
          />
          <Tile
            label="Total runs"
            value={String(b ? b.total : s.finished)}
            hint={!b && o.capped ? "the newest 500 counted" : undefined}
            points={o.buckets.map((x) => ({
              value: x.total,
              tip: `${bucketLabel(x.start, range)}: ${x.total} ${x.total === 1 ? "run" : "runs"}`,
            }))}
            tone="primary"
            at={at}
            period={b && bucketLabel(b.start, range)}
            onAt={setAt}
          />
          <Tile
            label="Average duration"
            value={(b ? duration(b.avgMs) : duration(s.avgMs)) || "—"}
            hint={!b && s.p95Ms ? `P50 ${duration(o.p50Ms) || "—"} · P95 ${duration(s.p95Ms)}` : undefined}
            points={o.buckets.map((x) => ({
              value: x.avgMs ?? 0,
              tip: `${bucketLabel(x.start, range)}: ${x.avgMs ? `${duration(x.avgMs)} average` : "no runs"}`,
            }))}
            tone="muted"
            at={at}
            period={b && bucketLabel(b.start, range)}
            onAt={setAt}
          />
        </div>
      )}
    </section>
  );
}
