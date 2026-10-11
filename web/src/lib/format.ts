// Formatting shared by every page — the same forms the server-rendered
// pages use, so both kinds of page read alike.

/** when renders a time as "2026-10-07 18:30:05 UTC" ("" for none). */
export function when(iso?: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toISOString().slice(0, 19).replace("T", " ") + " UTC";
}

/** duration renders milliseconds as "1h 2m", "3m 4s", "5.6s" ("" for 0). */
export function duration(ms?: number | null): string {
  if (!ms || ms <= 0) return "";
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${Math.floor(s) % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/** shortSha is the 7-character form of a commit SHA. */
export function shortSha(sha: string): string {
  return sha.slice(0, 7);
}

/** formatNumber renders a metric: integers as such, otherwise up to three decimals. */
export function formatNumber(v: number): string {
  if (Number.isInteger(v)) return String(v);
  return String(Number(v.toFixed(3)));
}
