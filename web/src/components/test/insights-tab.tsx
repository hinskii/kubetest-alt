import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { analyticsQuery, type TestKey } from "@/api/queries";
import { ErrorView } from "@/components/error-view";
import { PhaseBadge } from "@/components/phase-badge";
import { Panel } from "@/components/run/sections";
import { Trends } from "@/components/test/trends";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { duration } from "@/lib/format";

/**
 * InsightsTab: the Test's last 30 finished runs at a glance — summary,
 * each value's trend, flaky and failing test cases — with the way to the
 * full analysis pages. Loaded with the tab (Recharts).
 */
export default function InsightsTab({ k }: { k: TestKey }) {
  const a = useQuery(analyticsQuery(k, 30));
  const links = (
    <div className="flex flex-wrap gap-2">
      <Button variant="outline" size="sm" asChild>
        <Link to="/clusters/$cluster/tests/$ns/$name/analytics" params={k}>
          Analytics &amp; comparison
        </Link>
      </Button>
      <Button variant="outline" size="sm" asChild>
        <Link to="/clusters/$cluster/tests/$ns/$name/cases" params={k}>
          Test cases &amp; flaky tests
        </Link>
      </Button>
    </div>
  );
  if (a.isError) return <ErrorView error={a.error} />;
  if (a.isPending) return <Skeleton className="h-64 w-full" />;
  const d = a.data;
  const s = d.summary;
  return (
    <div className="space-y-6">
      <Panel id="insights-summary" title={`Last ${d.window} runs`} actions={links}>
        {d.runs.length === 0 ? (
          <p className="text-muted-foreground">
            No finished runs yet: insights appear once runs have finished.
          </p>
        ) : (
          <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
            <Stat label="Pass rate" value={s.passRate || "—"} />
            <Stat label="Passed · failed · error" value={`${s.passed} · ${s.failed} · ${s.errored}`} />
            <Stat label="Average" value={duration(s.avgMs) || "—"} />
            <Stat label="P95" value={duration(s.p95Ms) || "—"} />
            <Stat
              label="Last failure"
              value={
                s.lastFailure ? (
                  <Link
                    to="/clusters/$cluster/runs/$ns/$id"
                    params={{ cluster: k.cluster, ns: k.ns, id: s.lastFailure.id }}
                    className="text-primary inline-flex items-center gap-2 hover:underline"
                  >
                    {s.lastFailure.name} <PhaseBadge phase={s.lastFailure.phase} />
                  </Link>
                ) : (
                  "none"
                )
              }
            />
          </dl>
        )}
        {d.cases && (
          <p className="text-sm">
            Test cases: <strong>{d.cases.failingNow}</strong> failing in the latest run ·{" "}
            <strong>{d.cases.flaky}</strong> flaky —{" "}
            <Link
              to="/clusters/$cluster/tests/$ns/$name/cases"
              params={k}
              className="text-primary hover:underline"
            >
              see them
            </Link>
          </p>
        )}
      </Panel>
      {d.trends.length > 0 && <Trends trends={d.trends} />}
    </div>
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
