import { Line, LineChart, YAxis } from "recharts";

/** Sparkline is a value's trend, oldest → newest, with the latest marked. */
export function Sparkline({ values, label }: { values: number[]; label: string }) {
  const data = values.map((v, i) => ({ i, v }));
  const last = data.length - 1;
  return (
    <div role="img" aria-label={`${label} trend`}>
      <LineChart width={140} height={28} data={data} margin={{ top: 3, right: 3, bottom: 3, left: 3 }}>
        <YAxis hide domain={["dataMin", "dataMax"]} />
        <Line
          type="linear"
          dataKey="v"
          stroke="var(--primary)"
          strokeWidth={1.5}
          isAnimationActive={false}
          dot={(p: { index?: number; cx?: number; cy?: number }) =>
            p.index === last ? (
              <circle key="last" cx={p.cx} cy={p.cy} r={2.5} fill="var(--primary)" />
            ) : (
              <g key={p.index} />
            )
          }
        />
      </LineChart>
    </div>
  );
}
