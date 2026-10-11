import { Link } from "@tanstack/react-router";

import type { TestSummary } from "@/api/client";
import { duration, when } from "@/lib/format";
import { cn } from "@/lib/utils";

type Recent = NonNullable<TestSummary["stats"]>["recent"][number];

const tone: Record<string, string> = {
  passed: "bg-success",
  failed: "bg-danger",
  error: "bg-danger",
  aborted: "bg-unknown",
};

/**
 * RecentRuns are a Test's newest runs as marks, oldest → newest, each
 * coloured by its phase and a link to the run (step 20b).
 */
export function RecentRuns({ cluster, ns, runs }: { cluster: string; ns: string; runs: Recent[] }) {
  if (runs.length === 0) return <span className="text-muted-foreground">—</span>;
  return (
    <ol aria-label="Recent runs" className="flex items-center gap-0.5">
      {runs.map((r) => {
        const text = [r.id, r.phase, duration(r.durationMs), when(r.finishedAt)].filter(Boolean).join(" · ");
        return (
          <li key={r.id}>
            <Link
              to="/clusters/$cluster/runs/$ns/$id"
              params={{ cluster, ns, id: r.id }}
              aria-label={text}
              title={text}
              data-phase={r.phase}
              className={cn(
                "block h-4 w-1.5 rounded-sm hover:scale-y-125 focus-visible:outline-2",
                tone[r.phase] ?? "bg-muted-foreground",
              )}
            />
          </li>
        );
      })}
    </ol>
  );
}
