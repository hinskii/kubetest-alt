import type { Trend } from "@/api/client";
import { Panel } from "@/components/run/sections";
import { Sparkline } from "@/components/sparkline";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { duration, formatNumber } from "@/lib/format";

/** trendValue is one value of a trend in its unit. */
export function trendValue(t: Trend, v: number): string {
  return t.unit === "ms" ? duration(Math.round(v)) : formatNumber(v);
}

const value = trendValue;

/** Trends: each metric's values over the runs, with a sparkline (Recharts). */
export function Trends({ trends }: { trends: Trend[] }) {
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
