import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";
import { z } from "zod";

import { cleanup, cleanupQuery, clustersQuery, meQuery, type CleanupCriteria } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { PhaseBadge } from "@/components/phase-badge";
import { Panel } from "@/components/run/sections";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { duration, when } from "@/lib/format";

const search = z.object({
  mode: z.enum(["older", "range"]).optional().catch(undefined),
  // The router parses ?days=30 as a number.
  days: z.union([z.string(), z.number()]).transform(String).optional().catch(undefined),
  from: z.string().optional().catch(undefined),
  to: z.string().optional().catch(undefined),
});

export const Route = createFileRoute("/clusters/$cluster/tests/$ns/$name_/cleanup")({
  validateSearch: search,
  component: CleanupPage,
});

function CleanupPage() {
  const k = Route.useParams();
  const criteria = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const me = useQuery(meQuery);
  const clusters = useQuery(clustersQuery);
  const clusterTitle = clusters.data?.find((c) => c.name === k.cluster)?.displayName ?? k.cluster;

  // The form edits a draft; Preview puts it in the address, which runs it.
  const [mode, setMode] = useState<"older" | "range">(criteria.mode ?? "older");
  const [days, setDays] = useState(criteria.days ?? "30");
  const [from, setFrom] = useState(criteria.from ?? "");
  const [to, setTo] = useState(criteria.to ?? "");
  const submitted: CleanupCriteria | undefined = criteria.mode
    ? criteria.mode === "older"
      ? { mode: "older", days: criteria.days }
      : { mode: "range", from: criteria.from, to: criteria.to }
    : undefined;

  return (
    <div className="space-y-6">
      <PageHeader
        crumbs={[
          { label: "Clusters", to: "/" },
          { label: clusterTitle, to: "/clusters/$cluster", params: { cluster: k.cluster } },
          { label: k.name, to: "/clusters/$cluster/tests/$ns/$name", params: k },
          { label: "Clean up runs" },
        ]}
        title={`${k.name} · clean up runs`}
      />
      <p className="text-muted-foreground -mt-4 text-sm">
        Deletes finished runs of this Test with their logs, artifacts, test cases and comment. Run history
        also expires on its own after the retention period. Running and queued runs are never touched.
      </p>

      <Panel id="criteria" title="Which runs">
        <form
          aria-label="Which runs"
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void navigate({
              search:
                mode === "older" ? { mode, days } : { mode, from: from || undefined, to: to || undefined },
            });
          }}
        >
          <RadioGroup value={mode} onValueChange={(v) => setMode(v as "older" | "range")} className="gap-4">
            <div className="flex flex-wrap items-center gap-2">
              <RadioGroupItem value="older" id="mode-older" />
              <Label htmlFor="mode-older">Finished more than</Label>
              <Input
                aria-label="Days"
                type="number"
                min={1}
                max={3650}
                step={1}
                className="w-24"
                value={days}
                disabled={mode !== "older"}
                onChange={(e) => setDays(e.target.value)}
              />
              <span>days ago</span>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <RadioGroupItem value="range" id="mode-range" />
              <Label htmlFor="mode-range">
                Finished between{" "}
                <span className="text-muted-foreground font-normal">
                  dates, UTC, inclusive — at least one
                </span>
              </Label>
              <Input
                aria-label="From"
                type="date"
                className="w-40"
                value={from}
                disabled={mode !== "range"}
                onChange={(e) => setFrom(e.target.value)}
              />
              <Input
                aria-label="To"
                type="date"
                className="w-40"
                value={to}
                disabled={mode !== "range"}
                onChange={(e) => setTo(e.target.value)}
              />
            </div>
          </RadioGroup>
          <Button type="submit" variant="outline">
            Preview
          </Button>
        </form>
      </Panel>

      {submitted && <Preview k={k} criteria={submitted} canDelete={!!me.data?.can.delete} />}
    </div>
  );
}

function Preview({
  k,
  criteria,
  canDelete,
}: {
  k: { cluster: string; ns: string; name: string };
  criteria: CleanupCriteria;
  canDelete: boolean;
}) {
  const qc = useQueryClient();
  const preview = useQuery(cleanupQuery(k, criteria));
  const [unchecked, setUnchecked] = useState<Set<string>>(new Set());
  const del = useMutation({
    mutationFn: (ids: string[]) => cleanup(k, ids),
    onSuccess: (res) => {
      const msg = `Deleted ${res.deleted} of ${res.requested} runs.`;
      if (res.failures.length) toast.warning(`${msg} Not deleted: ${res.failures.join("; ")}`);
      else toast.success(msg);
      setUnchecked(new Set());
      return qc.invalidateQueries({ queryKey: ["clusters", k.cluster, "tests", k.ns, k.name] });
    },
    onError: (e) => toast.error(`Could not delete: ${e.message}`),
  });

  if (preview.isError) return <ErrorView error={preview.error} />;
  if (!preview.data) return <Skeleton className="h-32 w-full" />;
  const p = preview.data;
  const selected = p.matches.filter((r) => !unchecked.has(r.id)).map((r) => r.id);
  const toggle = (id: string, on: boolean) =>
    setUnchecked((s) => {
      const n = new Set(s);
      if (on) n.delete(id);
      else n.add(id);
      return n;
    });

  return (
    <Panel
      id="matches"
      title={
        <>
          Matching runs{" "}
          <span className="text-muted-foreground text-sm font-normal">
            {p.matches.length}
            {p.more ? ` (+${p.more} more)` : ""}
          </span>
        </>
      }
    >
      {p.note && <p className="text-muted-foreground text-sm">{p.note}</p>}
      {p.more ? (
        <p className="text-muted-foreground text-sm">
          One cleanup deletes at most {p.max} runs, oldest first — run it again for the rest.
        </p>
      ) : null}
      {p.scanCapped && (
        <p className="text-muted-foreground text-sm">
          Only the newest {p.scanned} runs were checked; older matches show up after this batch is gone.
        </p>
      )}
      {p.matches.length === 0 ? (
        <p className="text-muted-foreground">No finished runs match.</p>
      ) : (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-10" />
                <TableHead>Run</TableHead>
                <TableHead>Phase</TableHead>
                <TableHead>Finished</TableHead>
                <TableHead>Duration</TableHead>
                <TableHead>Note</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {p.matches.map((r) => (
                <TableRow key={r.id}>
                  <TableCell>
                    <Checkbox
                      aria-label={`delete ${r.name}`}
                      checked={!unchecked.has(r.id)}
                      disabled={!canDelete}
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
                  <TableCell className="text-muted-foreground">{when(r.finishedAt)}</TableCell>
                  <TableCell>{duration(r.durationMs)}</TableCell>
                  <TableCell className="max-w-xs truncate">{r.comment}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {canDelete ? (
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="destructive" disabled={selected.length === 0 || del.isPending}>
                  Delete selected runs
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Delete {selected.length} runs?</AlertDialogTitle>
                  <AlertDialogDescription>
                    Their logs, artifacts, test cases and comments go too. This cannot be undone; each delete
                    is recorded in the audit log.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    className="bg-destructive hover:bg-destructive/90 text-white"
                    onClick={() => del.mutate(selected)}
                  >
                    Delete {selected.length} runs
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          ) : (
            <p className="text-muted-foreground">Only admins can delete runs.</p>
          )}
        </>
      )}
    </Panel>
  );
}
