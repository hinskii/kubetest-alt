import { Check, CircleDashed, Loader2, Minus, X } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { Fragment, useEffect, useState, type ReactNode } from "react";

import type { RunDetail } from "@/api/client";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type Stage = RunDetail["stages"][number];
type Step = RunDetail["steps"][number];
type Child = RunDetail["children"][number];

const look: Record<string, { box: string; icon: React.ReactNode; label: string }> = {
  passed: { box: "bg-success-soft text-success", icon: <Check className="size-4" />, label: "passed" },
  failed: { box: "bg-danger-soft text-danger", icon: <X className="size-4" />, label: "failed" },
  error: { box: "bg-danger-soft text-danger", icon: <X className="size-4" />, label: "error" },
  aborted: { box: "bg-unknown-soft text-unknown", icon: <X className="size-4" />, label: "aborted" },
  running: {
    box: "bg-warning-soft text-warning",
    icon: <Loader2 className="size-4 animate-spin motion-reduce:animate-none" />,
    label: "running",
  },
  skipped: { box: "bg-muted text-muted-foreground", icon: <Minus className="size-4" />, label: "skipped" },
  queued: {
    box: "bg-muted text-muted-foreground",
    icon: <CircleDashed className="size-4" />,
    label: "waiting",
  },
};

/** useNow ticks every 50 ms while on: a running stage's time runs like a stopwatch. */
function useNow(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setNow(Date.now()), 50);
    return () => clearInterval(t);
  }, [on]);
  return now;
}

/** stageTime is a stage's time for its tile: 0.42s, 34.27s, 1m 05.3s, 1h 02m. */
export function stageTime(ms: number): string {
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`;
  if (ms < 3_600_000) {
    const m = Math.floor(ms / 60_000);
    return `${m}m ${((ms % 60_000) / 1000).toFixed(1).padStart(4, "0")}s`;
  }
  const h = Math.floor(ms / 3_600_000);
  return `${h}h ${String(Math.floor((ms % 3_600_000) / 60_000)).padStart(2, "0")}m`;
}

/** stageMs is a stage's duration: to its end, or to now while it runs. */
export function stageMs(
  s: { startedAt?: string | null; finishedAt?: string | null; phase: string },
  now: number,
): number | undefined {
  if (!s.startedAt) return undefined;
  const end = s.finishedAt ? Date.parse(s.finishedAt) : s.phase === "running" ? now : undefined;
  return end === undefined ? undefined : Math.max(0, end - Date.parse(s.startedAt));
}

/** A tile's content: what it is, its state, its time, why it ended so. */
type TileProps = {
  name: string;
  phase: string;
  startedAt?: string | null;
  finishedAt?: string | null;
  /** durationMs stands in for the times (a child run's). */
  durationMs?: number;
  message?: string;
  now: number;
};

function TileBody({ name, phase, startedAt, finishedAt, durationMs, message, now }: TileProps) {
  const l = look[phase] ?? look.queued!;
  const ms = startedAt ? stageMs({ startedAt, finishedAt, phase }, now) : durationMs;
  const bad = phase === "failed" || phase === "error" || phase === "aborted";
  return (
    <>
      <div className="flex items-center gap-2">
        <span className={cn("flex size-7 shrink-0 items-center justify-center rounded-md", l.box)}>
          {l.icon}
        </span>
        <span className="min-w-0 flex-1 leading-tight font-medium [overflow-wrap:anywhere]" title={name}>
          {name}
        </span>
        <span className="text-muted-foreground shrink-0 text-sm tabular-nums">
          {ms === undefined ? "" : ms === 0 && finishedAt ? "<1s" : stageTime(ms)}
        </span>
      </div>
      <span className="sr-only">{l.label}</span>
      {message && (
        <p
          className={cn("mt-2 text-sm break-words", bad ? "text-danger" : "text-muted-foreground")}
          title={message}
        >
          {message}
        </p>
      )}
    </>
  );
}

/** Tile is one box of a flow: picks a log (onClick) or leads to a run (to). */
function Tile({
  selected,
  onClick,
  label,
  link,
  ...body
}: TileProps & { selected?: boolean; onClick?: () => void; label?: string; link?: ReactNode }) {
  return (
    <li
      className={cn(
        "bg-card relative min-w-0 flex-1 rounded-lg border p-3",
        body.phase === "queued" || body.phase === "skipped" ? "opacity-70" : "",
        (onClick || link) && "hover:border-primary/60 focus-within:ring-ring/50 focus-within:ring-[3px]",
        selected && "border-primary ring-primary/30 ring-2",
      )}
      data-testid="stage"
      data-phase={body.phase}
    >
      {onClick && (
        <button
          type="button"
          className="absolute inset-0 rounded-lg outline-none"
          aria-pressed={!!selected}
          aria-label={label}
          onClick={onClick}
        />
      )}
      {link}
      <TileBody {...body} />
    </li>
  );
}

function Connector() {
  return (
    <li aria-hidden className="hidden w-10 shrink-0 items-center md:flex" data-testid="stage-connector">
      <span className="border-primary/60 w-full border-t-2 border-dotted" />
    </li>
  );
}

/**
 * RunFlow draws a run's course as tiles left to right — queued, fetching
 * the content, running the tool, collecting results — each with its state
 * and time, live while the run goes (step 20a).
 */
export function RunFlow({
  stages,
  className,
  selected,
  onSelect,
}: {
  stages: Stage[];
  className?: string;
  /** selected is the stage whose log is shown; onSelect makes tiles pick one. */
  selected?: string;
  onSelect?: (kind: Stage["kind"]) => void;
}) {
  const now = useNow(stages.some((s) => s.phase === "running"));
  if (stages.length === 0) return null;
  return (
    <ol
      aria-label="Run stages"
      className={cn("flex flex-col gap-2 md:flex-row md:items-stretch md:gap-0", className)}
    >
      {stages.map((s, i) => (
        <Fragment key={s.kind}>
          {i > 0 && <Connector />}
          <Tile
            {...s}
            now={now}
            selected={selected === s.kind}
            label={`Show the log of ${s.name}`}
            onClick={onSelect && (() => onSelect(s.kind))}
          />
        </Fragment>
      ))}
    </ol>
  );
}

/** workerSteps are a parallel run's workers, in order. */
export function workerSteps(steps: Step[]): Step[] {
  return steps.filter((s) => s.kind === "Worker");
}

/** workerName is a worker step's name: "worker-1" → "Worker 1". */
export function workerName(key: string): string {
  return key.replace("worker-", "Worker ");
}

/** paramsText is a worker's matrix and shard values: "browser=chrome, shard=1". */
export function paramsText(params?: Record<string, string>): string {
  return Object.entries(params ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}=${v}`)
    .join(", ");
}

/** workerLabel names a worker with its values: "Worker 1 · vus=5". */
export function workerLabel(w: Step): string {
  return [workerName(w.key), paramsText(w.params)].filter(Boolean).join(" · ");
}

/**
 * WorkerPicker picks which parallel worker a tab shows (its log, live
 * view or report); one picked for all of them.
 */
export function WorkerPicker({
  workers,
  selected,
  onSelect,
}: {
  workers: Step[];
  selected?: string;
  onSelect: (key: string) => void;
}) {
  if (workers.length === 0) return null;
  return (
    <div role="group" aria-label="Worker" className="flex flex-wrap gap-2">
      {workers.map((w) => (
        <Button
          key={w.key}
          size="sm"
          variant={w.key === selected ? "default" : "outline"}
          aria-pressed={w.key === selected}
          onClick={() => onSelect(w.key)}
        >
          {workerLabel(w)}
        </Button>
      ))}
    </div>
  );
}

/**
 * WorkersFlow draws a parallel run's workers, one row each: the worker,
 * its matrix and shard values, and its own stages, live. A stage shows
 * that worker's log of it (selected: "worker-N/<stage>"); a worker from
 * before stages were kept is one tile (selected: "worker-N").
 */
export function WorkersFlow({
  steps,
  selected,
  onSelect,
}: {
  steps: Step[];
  selected?: string;
  onSelect?: (key: string) => void;
}) {
  const workers = workerSteps(steps);
  const now = useNow(
    workers.some((s) => s.phase === "running" || s.stages?.some((st) => st.phase === "running")),
  );
  if (workers.length === 0) return null;
  const passed = workers.filter((w) => w.phase === "passed").length;
  return (
    <section aria-label="Workers" className="space-y-2">
      <h2 className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
        Workers · {passed} of {workers.length} passed
      </h2>
      <ol className="space-y-3">
        {workers.map((w) => {
          const name = workerName(w.key);
          const params = paramsText(w.params);
          const stages = w.stages ?? [];
          if (stages.length === 0) {
            return (
              <Tile
                key={w.key}
                name={params ? `${name} · ${params}` : name}
                phase={w.phase ?? "queued"}
                startedAt={w.startedAt}
                finishedAt={w.finishedAt}
                message={w.message}
                now={now}
                selected={selected === w.key}
                label={`Show the log of ${name.toLowerCase()}`}
                onClick={onSelect && (() => onSelect(w.key))}
              />
            );
          }
          const ms = stageMs({ ...w, phase: w.phase ?? "queued" }, now);
          return (
            <li key={w.key} className="space-y-1.5" data-testid="worker" data-phase={w.phase ?? "queued"}>
              <div className="flex flex-wrap items-baseline gap-x-2 text-sm">
                <span className="font-medium">{name}</span>
                {params && <span className="text-muted-foreground font-mono text-xs">{params}</span>}
                <span className="text-muted-foreground">
                  · {(look[w.phase ?? "queued"] ?? look.queued!).label}
                </span>
                {ms !== undefined && (
                  <span className="text-muted-foreground tabular-nums">· {stageTime(ms)}</span>
                )}
              </div>
              <ol
                aria-label={`${name} stages`}
                className="flex flex-col gap-2 md:flex-row md:items-stretch md:gap-0"
              >
                {stages.map((s, i) => (
                  <Fragment key={s.kind}>
                    {i > 0 && <Connector />}
                    <Tile
                      {...s}
                      now={now}
                      selected={selected === `${w.key}/${s.kind}`}
                      label={`Show the log of ${name.toLowerCase()}: ${s.name}`}
                      onClick={onSelect && (() => onSelect(`${w.key}/${s.kind}`))}
                    />
                  </Fragment>
                ))}
              </ol>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

/**
 * ChildrenFlow draws a composite run's child runs in order, each a link to
 * the child run.
 */
export function ChildrenFlow({ cluster, ns, items }: { cluster: string; ns: string; items: Child[] }) {
  const now = useNow(items.some((c) => c.phase === "running"));
  if (items.length === 0) return null;
  return (
    <ol aria-label="Child runs" className="flex flex-col gap-2 md:flex-row md:items-stretch md:gap-0">
      {items.map((c, i) => (
        <Fragment key={c.id}>
          {i > 0 && <Connector />}
          <Tile
            name={c.testRef}
            phase={c.phase}
            startedAt={c.phase === "running" ? c.startedAt : undefined}
            durationMs={c.durationMs}
            message={c.name}
            now={now}
            link={
              <Link
                to="/clusters/$cluster/runs/$ns/$id"
                params={{ cluster, ns, id: c.id }}
                className="absolute inset-0 rounded-lg outline-none"
                aria-label={`Open ${c.name} (${c.testRef}, ${c.phase})`}
              />
            }
          />
        </Fragment>
      ))}
    </ol>
  );
}
