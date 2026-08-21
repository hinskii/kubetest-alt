// Time formatting for dense tables. No moment.js, no dayjs — Intl is
// enough. Two shapes: "N min ago" for the runs list (recency signal),
// and "YYYY-MM-DD HH:MM:SS" for detail views (precise).
export function relativeTime(iso?: string, now = Date.now()): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return iso
  const secs = Math.round((now - t) / 1000)
  if (secs < 60) return `${secs}s ago`
  if (secs < 3600) return `${Math.round(secs / 60)}m ago`
  if (secs < 86400) return `${Math.round(secs / 3600)}h ago`
  return `${Math.round(secs / 86400)}d ago`
}

export function absTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  )
}

export function durationMs(ms?: number): string {
  if (ms === undefined || ms === null) return '—'
  if (ms < 1000) return `${ms}ms`
  const secs = ms / 1000
  if (secs < 60) return `${secs.toFixed(2)}s`
  const m = Math.floor(secs / 60)
  const s = Math.round(secs % 60)
  return `${m}m${s.toString().padStart(2, '0')}s`
}

// durationShort trades precision for scannability. Used in the runs
// list where the reader wants "roughly how long" at a glance, not
// millisecond-precise wall-clock. Rules:
//   < 1s → "0.Ns"                (Nms is noise at scale)
//   < 10s → "N.Ns" (1 decimal)   (8.0s not 8.04s)
//   < 60s → "Ns"                 (14s not 14.04s)
//   < 60m → "NmSSs"              (1m30s)
//   ≥ 60m → "NhMMm"              (1h30m)
export function durationShort(ms?: number): string {
  if (ms === undefined || ms === null) return '—'
  if (ms < 1000) return `${(ms / 1000).toFixed(1)}s`
  const secs = ms / 1000
  if (secs < 10) return `${secs.toFixed(1)}s`
  if (secs < 60) return `${Math.round(secs)}s`
  if (secs < 3600) {
    const m = Math.floor(secs / 60)
    const s = Math.round(secs % 60)
    return `${m}m${s.toString().padStart(2, '0')}s`
  }
  const h = Math.floor(secs / 3600)
  const m = Math.floor((secs % 3600) / 60)
  return `${h}h${m.toString().padStart(2, '0')}m`
}
