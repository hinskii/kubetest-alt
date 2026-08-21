import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { PageHeader } from '../components/PageHeader'
import { PhaseChip } from '../components/PhaseChip'
import { EmptyState, ErrorState, Loading } from '../components/States'
import { durationMs, relativeTime } from '../components/TimeCells'

// Runs list is the merged cluster+archive stream (apiserver merges +
// sorts server-side). Filter drop-downs cover phase and source; text
// filter covers name + test name. Pagination step: the apiserver
// accepts `limit` but hasn't grown a next_cursor field yet (see
// internal/apiserver/handlers_runs.go). We ask for 200, show a warning
// when we get exactly 200 back, and route users to filters. Keyset
// pagination is a backend follow-up.

const PAGE_LIMIT = 200

const PHASES = [
  '',
  'queued',
  'running',
  'passed',
  'failed',
  'error',
  'aborted',
] as const

const SOURCES = ['', 'api', 'ui', 'cli', 'cron', 'trigger', 'gitops'] as const

export default function RunsList() {
  const [phase, setPhase] = useState<string>('')
  const [source, setSource] = useState<string>('')
  const [nameFilter, setNameFilter] = useState('')

  const q = useQuery({
    queryKey: ['runs', { phase, source, limit: PAGE_LIMIT }],
    queryFn: () => api.listRuns({ phase, limit: PAGE_LIMIT }),
    refetchInterval: 10_000,
  })

  const rows = useMemo(() => {
    const items = q.data ?? []
    return items.filter((r) => {
      if (source && r.source !== source) return false
      if (nameFilter) {
        const n = nameFilter.toLowerCase()
        if (
          !r.name.toLowerCase().includes(n) &&
          !(r.testRef ?? '').toLowerCase().includes(n)
        ) {
          return false
        }
      }
      return true
    })
  }, [q.data, source, nameFilter])

  const capped = q.isSuccess && (q.data?.length ?? 0) >= PAGE_LIMIT

  return (
    <>
      <PageHeader
        eyebrow="stream"
        title="Runs"
        meta={
          <>
            <label className="text-xs text-subtle tracking-wide uppercase flex items-center gap-2">
              phase
              <select
                aria-label="filter by phase"
                value={phase}
                onChange={(e) => setPhase(e.target.value)}
                className="border border-rule bg-white px-2 py-1 text-sm"
              >
                {PHASES.map((p) => (
                  <option key={p} value={p}>
                    {p || 'all'}
                  </option>
                ))}
              </select>
            </label>
            <label className="text-xs text-subtle tracking-wide uppercase flex items-center gap-2">
              source
              <select
                aria-label="filter by source"
                value={source}
                onChange={(e) => setSource(e.target.value)}
                className="border border-rule bg-white px-2 py-1 text-sm"
              >
                {SOURCES.map((s) => (
                  <option key={s} value={s}>
                    {s || 'all'}
                  </option>
                ))}
              </select>
            </label>
            <input
              type="text"
              aria-label="filter runs by name"
              placeholder="filter by name or test…"
              value={nameFilter}
              onChange={(e) => setNameFilter(e.target.value)}
              className="border border-rule px-2 py-1 text-sm bg-white w-64"
            />
          </>
        }
      />
      <div className="p-6 flex-1 overflow-auto">
        {q.isLoading && <Loading what="runs" />}
        {q.isError && (
          <ErrorState
            title="Couldn't load runs"
            detail={(q.error as Error).message}
          />
        )}
        {q.isSuccess && rows.length === 0 && (
          <EmptyState
            what={
              phase || source || nameFilter ? 'matching runs' : 'runs yet'
            }
            hint={
              phase || source || nameFilter
                ? 'Try broader filters.'
                : 'Trigger a Test via CLI or wait for a schedule/trigger.'
            }
          />
        )}
        {q.isSuccess && rows.length > 0 && (
          <>
            <table className="dense">
              <thead>
                <tr>
                  <th>name</th>
                  <th>test</th>
                  <th>phase</th>
                  <th>duration</th>
                  <th>source</th>
                  <th>origin</th>
                  <th>started</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.uid}>
                    <td className="font-semibold">
                      <Link to={`/runs/${encodeURIComponent(r.uid)}`}>
                        <span className="font-mono">{r.name}</span>
                      </Link>
                    </td>
                    <td>
                      <Link to={`/tests/${encodeURIComponent(r.testRef)}`}>
                        {r.testRef}
                      </Link>
                    </td>
                    <td>
                      <PhaseChip phase={r.phase} />
                    </td>
                    <td>{durationMs(r.durationMs)}</td>
                    <td className="text-xs uppercase tracking-wide">
                      {r.source ?? '—'}
                    </td>
                    <td className="text-xs uppercase tracking-wide">
                      {r.origin}
                    </td>
                    <td className="text-xs text-subtle">
                      {relativeTime(r.startedAt ?? r.queuedAt)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {capped && (
              <div className="mt-4 text-xs text-subtle uppercase tracking-wide">
                showing first {PAGE_LIMIT} — narrow with filters
              </div>
            )}
          </>
        )}
      </div>
    </>
  )
}
