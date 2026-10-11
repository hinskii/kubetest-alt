import { cn } from "@/lib/utils";

const tone: Record<string, string> = {
  passed: "bg-success-soft text-success",
  failed: "bg-danger-soft text-danger",
  error: "bg-danger-soft text-danger",
  running: "bg-warning-soft text-warning",
  queued: "bg-warning-soft text-warning",
  paused: "bg-warning-soft text-warning",
  aborted: "bg-unknown-soft text-unknown",
};

/** PhaseBadge shows a run phase in its colour (as app.css's status-*). */
export function PhaseBadge({ phase, className }: { phase: string; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium",
        tone[phase] ?? "bg-unknown-soft text-unknown",
        className,
      )}
      data-phase={phase}
    >
      {phase}
    </span>
  );
}

/** Chip is a small label (tool, read-only, not ready). */
export function Chip({
  children,
  tone: t = "neutral",
  className,
  ...rest
}: React.ComponentProps<"span"> & { tone?: "neutral" | "warning" }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium",
        t === "warning" ? "bg-warning-soft text-warning" : "bg-secondary text-secondary-foreground",
        className,
      )}
      {...rest}
    >
      {children}
    </span>
  );
}
