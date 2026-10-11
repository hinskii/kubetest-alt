import { Check, CircleDashed, Loader2, Minus, X } from "lucide-react";
import { Fragment, useEffect, useState } from "react";

import type { RunDetail } from "@/api/client";
import { cn } from "@/lib/utils";

type Stage = RunDetail["stages"][number];

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
export function stageMs(s: Stage, now: number): number | undefined {
  if (!s.startedAt) return undefined;
  const end = s.finishedAt ? Date.parse(s.finishedAt) : s.phase === "running" ? now : undefined;
  return end === undefined ? undefined : Math.max(0, end - Date.parse(s.startedAt));
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
  selected?: Stage["kind"];
  onSelect?: (kind: Stage["kind"]) => void;
}) {
  const now = useNow(stages.some((s) => s.phase === "running"));
  if (stages.length === 0) return null;
  return (
    <ol
      aria-label="Run stages"
      className={cn("flex flex-col gap-2 md:flex-row md:items-stretch md:gap-0", className)}
    >
      {stages.map((s, i) => {
        const l = look[s.phase] ?? look.queued!;
        const ms = stageMs(s, now);
        return (
          <Fragment key={s.kind}>
            {i > 0 && (
              <li
                aria-hidden
                className="hidden w-10 shrink-0 items-center md:flex"
                data-testid="stage-connector"
              >
                <span className="border-primary/60 w-full border-t-2 border-dotted" />
              </li>
            )}
            <li
              className={cn(
                "bg-card relative min-w-0 flex-1 rounded-lg border p-3",
                s.phase === "queued" || s.phase === "skipped" ? "opacity-70" : "",
                onSelect && "hover:border-primary/60 focus-within:ring-ring/50 focus-within:ring-[3px]",
                selected === s.kind && "border-primary ring-primary/30 ring-2",
              )}
              data-testid="stage"
              data-phase={s.phase}
            >
              {onSelect && (
                <button
                  type="button"
                  className="absolute inset-0 rounded-lg outline-none"
                  aria-pressed={selected === s.kind}
                  aria-label={`Show the log of ${s.name}`}
                  onClick={() => onSelect(s.kind)}
                />
              )}
              <div className="flex items-center gap-2">
                <span className={cn("flex size-7 shrink-0 items-center justify-center rounded-md", l.box)}>
                  {l.icon}
                </span>
                <span className="min-w-0 flex-1 truncate font-medium" title={s.name}>
                  {s.name}
                </span>
                <span className="text-muted-foreground shrink-0 text-sm tabular-nums">
                  {ms === undefined ? "" : ms === 0 && s.finishedAt ? "<1s" : stageTime(ms)}
                </span>
              </div>
              <span className="sr-only">{l.label}</span>
              {s.message && (
                <p className="text-danger mt-2 text-sm break-words" title={s.message}>
                  {s.message}
                </p>
              )}
            </li>
          </Fragment>
        );
      })}
    </ol>
  );
}
