import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import type { RunDetail, RunEvent } from "@/api/client";
import { PhaseBadge } from "@/components/phase-badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { duration, formatNumber, shortSha, when } from "@/lib/format";
import { artifactUrl } from "@/lib/paths";
import { cn } from "@/lib/utils";

export function Panel({
  title,
  actions,
  children,
  id,
}: {
  title: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  id: string;
}) {
  return (
    <section className="bg-card space-y-4 rounded-xl border p-5" aria-labelledby={`${id}-title`}>
      <div className="flex flex-wrap items-center gap-2">
        <h2 id={`${id}-title`} className="mr-auto text-lg font-semibold">
          {title}
        </h2>
        {actions}
      </div>
      {children}
    </section>
  );
}

function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="break-words">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

/** RunFacts: where the run came from, when, what it checked out and counted. */
export function RunFacts({ cluster, run }: { cluster: string; run: RunDetail }) {
  const rows: [string, ReactNode][] = [
    [
      "Test",
      <>
        <Link
          to="/clusters/$cluster/tests/$ns/$name"
          params={{ cluster, ns: run.namespace, name: run.testRef }}
          className="text-primary hover:underline"
        >
          {run.testRef}
        </Link>{" "}
        <span className="text-muted-foreground">{run.namespace}</span>
      </>,
    ],
  ];
  if (run.parentRun)
    rows.push([
      "Part of",
      <Link
        to="/clusters/$cluster/runs/$ns/$id"
        params={{ cluster, ns: run.namespace, id: run.parentRun }}
        className="text-primary hover:underline"
      >
        {run.parentRun}
      </Link>,
    ]);
  rows.push(["Source", [run.source, run.createdBy].filter(Boolean).join(" · ")]);
  if (run.notBefore) rows.push(["Scheduled", when(run.notBefore)]);
  if (run.queuedAt) rows.push(["Queued", when(run.queuedAt)]);
  if (run.startedAt) rows.push(["Started", when(run.startedAt)]);
  if (run.finishedAt)
    rows.push([
      "Finished",
      <>
        {when(run.finishedAt)} <span className="text-muted-foreground">{duration(run.durationMs)}</span>
      </>,
    ]);
  if (run.git) {
    const c = run.git.commit;
    rows.push([
      "Commit",
      <>
        {c.url ? (
          <a
            href={c.url}
            target="_blank"
            rel="noopener noreferrer"
            title={c.sha}
            className="font-mono hover:underline"
          >
            {shortSha(c.sha)}
          </a>
        ) : (
          <span className="font-mono" title={c.sha}>
            {shortSha(c.sha)}
          </span>
        )}
        {run.git.revision && <span className="text-muted-foreground"> · {run.git.revision}</span>}
        {run.git.repository && <span className="text-muted-foreground"> · {run.git.repository}</span>}
      </>,
    ]);
  }
  if (run.testCounts) {
    const t = run.testCounts;
    rows.push(["Tests", `${t.passed} passed · ${t.failed} failed · ${t.skipped} skipped · ${t.total} total`]);
  }
  if (run.abort)
    rows.push([
      "Abort",
      `${run.abort.reason}${run.abort.requestedBy ? ` by ${run.abort.requestedBy}` : ""}${run.abort.message ? `: ${run.abort.message}` : ""}`,
    ]);
  if (run.archived)
    rows.push([
      "Record",
      <span className="text-muted-foreground">run history (the TestRun is gone from the cluster)</span>,
    ]);
  const config = Object.entries(run.config).sort(([a], [b]) => a.localeCompare(b));
  return (
    <div className="grid gap-6 lg:grid-cols-3">
      <Facts rows={rows} />
      {config.length > 0 && (
        <div className="space-y-1.5">
          <h3 className="font-semibold">Parameters</h3>
          <Facts rows={config.map(([k, v]) => [k, <span className="font-mono">{v}</span>])} />
        </div>
      )}
      {run.metrics.length > 0 && (
        <div className="space-y-1.5">
          <h3 className="font-semibold">Metrics</h3>
          <Facts
            rows={run.metrics.map((m) => [
              m.name,
              <span className="font-mono">{formatNumber(m.value)}</span>,
            ])}
          />
        </div>
      )}
    </div>
  );
}

/** Activity is the newest event of a live run — why a queued run waits. */
export function Activity({ event }: { event: RunEvent }) {
  return (
    <p
      role="status"
      title={event.object}
      className={cn(
        "rounded-lg border px-3 py-2 text-sm",
        event.type === "Warning" ? "bg-warning-soft text-warning" : "bg-muted",
      )}
    >
      Now: <strong>{event.reason}</strong> — {event.message}{" "}
      <span className="text-muted-foreground text-xs">since {when(event.time)}</span>
    </p>
  );
}

export function Steps({ run }: { run: RunDetail }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead />
          <TableHead>Name</TableHead>
          <TableHead>Phase</TableHead>
          <TableHead>Started</TableHead>
          <TableHead>Finished</TableHead>
          <TableHead>Message</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {run.steps.map((s) => (
          <TableRow key={s.key}>
            <TableCell className="text-muted-foreground">{s.kind}</TableCell>
            <TableCell>{s.key}</TableCell>
            <TableCell>{s.phase && <PhaseBadge phase={s.phase} />}</TableCell>
            <TableCell className="text-muted-foreground">{when(s.startedAt)}</TableCell>
            <TableCell className="text-muted-foreground">{when(s.finishedAt)}</TableCell>
            <TableCell className="whitespace-normal">{s.message}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export function Children({ cluster, run }: { cluster: string; run: RunDetail }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Run</TableHead>
          <TableHead>Test</TableHead>
          <TableHead>Phase</TableHead>
          <TableHead>Started</TableHead>
          <TableHead>Duration</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {run.children.map((c) => (
          <TableRow key={c.id}>
            <TableCell>
              <Link
                to="/clusters/$cluster/runs/$ns/$id"
                params={{ cluster, ns: run.namespace, id: c.id }}
                className="text-primary hover:underline"
              >
                {c.name}
              </Link>
            </TableCell>
            <TableCell>
              <Link
                to="/clusters/$cluster/tests/$ns/$name"
                params={{ cluster, ns: run.namespace, name: c.testRef }}
                className="text-primary hover:underline"
              >
                {c.testRef}
              </Link>
            </TableCell>
            <TableCell>
              <PhaseBadge phase={c.phase} />
            </TableCell>
            <TableCell className="text-muted-foreground">{when(c.startedAt)}</TableCell>
            <TableCell>{duration(c.durationMs)}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export function FailedCases({ cluster, run }: { cluster: string; run: RunDetail }) {
  return (
    <ul className="divide-y">
      {run.failedCases.map((c) => (
        <li key={c.key} className="space-y-1 py-3 first:pt-0">
          <div className="flex flex-wrap items-center gap-2">
            <PhaseBadge phase={c.status} />
            <Link
              to="/clusters/$cluster/tests/$ns/$name/cases/history"
              params={{ cluster, ns: run.namespace, name: run.testRef }}
              search={{ case: c.key }}
              className="text-primary font-medium hover:underline"
              title="History of this test case"
            >
              {c.name}
            </Link>
            <span className="text-muted-foreground text-xs">
              {c.class || c.suite}
              {c.durationMs ? ` · ${duration(c.durationMs)}` : ""}
            </span>
          </div>
          {c.message && <p className="text-sm">{c.message}</p>}
          {c.details && (
            <details>
              <summary className="text-muted-foreground cursor-pointer text-sm">Details</summary>
              <pre className="bg-muted mt-1 overflow-auto rounded p-2 font-mono text-xs">{c.details}</pre>
            </details>
          )}
        </li>
      ))}
    </ul>
  );
}

export function Gallery({ cluster, run }: { cluster: string; run: RunDetail }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {run.media.map((m) => {
        const src = artifactUrl(cluster, run.namespace, run.id, m.path);
        return (
          <figure key={m.path} className="space-y-1">
            {m.video ? (
              // Screen recordings of a test run: no speech, so no captions.
              // eslint-disable-next-line jsx-a11y/media-has-caption
              <video controls preload="metadata" src={src} className="w-full rounded border" />
            ) : (
              <a href={src} target="_blank" rel="noopener noreferrer">
                <img loading="lazy" alt={m.path} src={src} className="w-full rounded border" />
              </a>
            )}
            <figcaption className="text-muted-foreground text-xs break-all">{m.path}</figcaption>
          </figure>
        );
      })}
    </div>
  );
}

export function Artifacts({ cluster, run }: { cluster: string; run: RunDetail }) {
  if (run.artifactsError)
    return <p className="text-muted-foreground">Artifacts unavailable: {run.artifactsError}</p>;
  if (run.artifacts.length === 0) return <p className="text-muted-foreground">None.</p>;
  return (
    <ul className="space-y-1 text-sm">
      {run.artifacts.map((a) => (
        <li key={a.path} className="flex flex-wrap items-baseline gap-2">
          <a
            href={artifactUrl(cluster, run.namespace, run.id, a.path)}
            target="_blank"
            rel="noopener noreferrer"
            className="text-primary break-all hover:underline"
          >
            {a.path}
          </a>
          <span className="text-muted-foreground text-xs">{a.contentType}</span>
          <a
            href={artifactUrl(cluster, run.namespace, run.id, a.path, true)}
            className="text-muted-foreground text-xs hover:underline"
          >
            download
          </a>
        </li>
      ))}
    </ul>
  );
}

/** Events: the run's Kubernetes events, open while live or with warnings. */
export function Events({ run }: { run: RunDetail }) {
  const warnings = run.events.filter((e) => e.type === "Warning").length;
  return (
    <details className="bg-card rounded-xl border p-5" open={!run.finished || warnings > 0}>
      <summary className="cursor-pointer text-lg font-semibold">
        Kubernetes events{" "}
        <span className="text-muted-foreground text-sm font-normal">
          {run.events.length}
          {warnings > 0 && ` · ${warnings} warning${warnings > 1 ? "s" : ""}`}
        </span>
      </summary>
      <div className="mt-4 space-y-2">
        {run.eventsError && <p className="text-muted-foreground">Events unavailable: {run.eventsError}</p>}
        {run.events.length > 0 && (
          <>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Time</TableHead>
                  <TableHead>Reason</TableHead>
                  <TableHead>Object</TableHead>
                  <TableHead>Message</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {run.events.map((e, i) => (
                  <TableRow key={i} className={cn(e.type === "Warning" && "bg-warning-soft text-warning")}>
                    <TableCell className="text-muted-foreground">{when(e.time)}</TableCell>
                    <TableCell>
                      <strong>{e.reason}</strong>
                      {e.count && e.count > 1 ? (
                        <span className="text-muted-foreground"> ×{e.count}</span>
                      ) : null}
                    </TableCell>
                    <TableCell className="font-mono text-xs">{e.object}</TableCell>
                    <TableCell className="whitespace-normal">{e.message}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <p className="text-muted-foreground text-xs">
              From Kubernetes, which keeps events for about an hour.
            </p>
          </>
        )}
      </div>
    </details>
  );
}
