import { Link } from "@tanstack/react-router";

import type { RunRow } from "@/api/client";
import { PhaseBadge } from "@/components/phase-badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { duration, shortSha, when } from "@/lib/format";

/** RunsTable is a Test's run history (one page). */
export function RunsTable({ cluster, ns, runs }: { cluster: string; ns: string; runs: RunRow[] }) {
  const showCommit = runs.some((r) => r.commit);
  return (
    <div className="bg-card rounded-xl border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Run</TableHead>
            <TableHead>Phase</TableHead>
            <TableHead>Started</TableHead>
            <TableHead>Duration</TableHead>
            {showCommit && <TableHead>Commit</TableHead>}
            <TableHead>Source</TableHead>
            <TableHead>Note</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {runs.map((r) => (
            <TableRow key={r.id}>
              <TableCell>
                <Link
                  to="/clusters/$cluster/runs/$ns/$id"
                  params={{ cluster, ns, id: r.id }}
                  className="text-primary font-medium hover:underline"
                >
                  {r.name}
                </Link>
              </TableCell>
              <TableCell>
                <PhaseBadge phase={r.phase} />
              </TableCell>
              <TableCell className="text-muted-foreground">
                {r.startedAt ? when(r.startedAt) : r.notBefore ? `scheduled ${when(r.notBefore)}` : ""}
              </TableCell>
              <TableCell>{duration(r.durationMs)}</TableCell>
              {showCommit && (
                <TableCell className="font-mono">
                  {r.commit &&
                    (r.commit.url ? (
                      <a
                        href={r.commit.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        title={r.commit.sha}
                        className="hover:underline"
                      >
                        {shortSha(r.commit.sha)}
                      </a>
                    ) : (
                      <span title={r.commit.sha}>{shortSha(r.commit.sha)}</span>
                    ))}
                </TableCell>
              )}
              <TableCell className="text-muted-foreground">
                {[r.source, r.createdBy].filter(Boolean).join(" · ")}
              </TableCell>
              <TableCell className="max-w-xs truncate" title={r.waiting ?? r.comment}>
                {r.waiting ? <span className="text-muted-foreground italic">{r.waiting}</span> : r.comment}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
