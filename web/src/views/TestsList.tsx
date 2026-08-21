import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { PageHeader } from '../components/PageHeader'
import { PhaseChip } from '../components/PhaseChip'
import { ManagedByBadge } from '../components/ManagedByBadge'
import { EmptyState, ErrorState, Loading } from '../components/States'
import { relativeTime } from '../components/TimeCells'

// The Tests view is the entry point — first hit after loading. It
// leans on the fact that most engineering-cluster inventories are
// dozens, not hundreds, of Tests — one big dense table, one input for
// text filter. Sorting is fixed (kubectl-style: name asc).
type TestObj = {
  metadata?: {
    name?: string
    labels?: Record<string, string>
    creationTimestamp?: string
  }
  spec?: {
    schedule?: string
    steps?: unknown[]
    use?: string[]
    container?: { image?: string }
  }
  status?: {
    latestRun?: { name?: string; phase?: string; finishedAt?: string }
  }
}

export default function TestsList() {
  const q = useQuery({ queryKey: ['tests'], queryFn: () => api.listTests() })
  const [filter, setFilter] = useState('')

  const rows = useMemo<TestObj[]>(() => {
    const items = (q.data ?? []) as unknown as TestObj[]
    const needle = filter.trim().toLowerCase()
    if (!needle) return items
    return items.filter((t) => {
      const name = t.metadata?.name ?? ''
      const tool = t.metadata?.labels?.['kubetest.io/tool'] ?? ''
      return (
        name.toLowerCase().includes(needle) ||
        tool.toLowerCase().includes(needle)
      )
    })
  }, [q.data, filter])

  return (
    <>
      <PageHeader
        eyebrow="index"
        title="Tests"
        meta={
          <input
            type="text"
            aria-label="filter tests"
            placeholder="filter by name or tool…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            className="border border-rule px-2 py-1 text-sm bg-white w-64"
          />
        }
      />
      <div className="p-6 flex-1 overflow-auto">
        {q.isLoading && <Loading what="tests" />}
        {q.isError && (
          <ErrorState
            title="Couldn't load tests"
            detail={(q.error as Error).message}
          />
        )}
        {q.isSuccess && rows.length === 0 && (
          <EmptyState
            what={filter ? 'matching tests' : 'tests'}
            hint={
              filter
                ? 'Clear the filter, or check the tool label spelling.'
                : 'Apply a Test manifest via kubectl or ArgoCD to see it here.'
            }
          />
        )}
        {q.isSuccess && rows.length > 0 && (
          <table className="dense">
            <thead>
              <tr>
                <th>name</th>
                <th>tool</th>
                <th>managed</th>
                <th>shape</th>
                <th>schedule</th>
                <th>last run</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((t) => {
                const name = t.metadata?.name ?? '—'
                const rawTool = t.metadata?.labels?.['kubetest.io/tool']
                const managedBy =
                  t.metadata?.labels?.['app.kubernetes.io/managed-by']
                const shape = shapeOf(t)
                const usesTemplate = (t.spec?.use ?? []).length > 0
                // Composite parents have no single tool identity — the
                // "composite" value is a metric label, NOT a UI value.
                // Render dash so the column doesn't lie about content.
                const toolDisplay =
                  shape === 'composite' || !rawTool || rawTool === 'composite'
                    ? '—'
                    : rawTool
                const last = t.status?.latestRun
                return (
                  <tr key={name} aria-disabled={managedBy === 'gitops'}>
                    <td className="font-semibold">
                      <Link to={`/tests/${encodeURIComponent(name)}`}>
                        {name}
                      </Link>
                    </td>
                    <td>
                      <span className="inline-flex items-center gap-2">
                        {toolDisplay}
                        {usesTemplate && (
                          <span
                            role="img"
                            aria-label="delivered by template"
                            title="delivered by template (spec.use)"
                            className="text-xs text-subtle"
                          >
                            ↺
                          </span>
                        )}
                      </span>
                    </td>
                    <td>
                      <ManagedByBadge value={managedBy} />
                      {!managedBy && (
                        <span className="text-xs text-subtle uppercase tracking-wide">
                          ui
                        </span>
                      )}
                    </td>
                    <td className="text-xs text-subtle uppercase tracking-wide">
                      {shape}
                    </td>
                    <td>{t.spec?.schedule || '—'}</td>
                    <td>
                      {last?.phase ? (
                        <span className="inline-flex items-center gap-3">
                          <PhaseChip phase={last.phase} />
                          <span className="text-xs text-subtle">
                            {relativeTime(last.finishedAt)}
                          </span>
                        </span>
                      ) : (
                        <span className="text-subtle text-sm">never</span>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>
    </>
  )
}

// Shape is a binary categorization: composite (has spec.steps) vs
// leaf (everything else). Templates are a DELIVERY mechanism — they
// contribute container/args/verdict — not a third shape. The template
// indicator surfaces next to the tool cell instead.
function shapeOf(t: TestObj): 'leaf' | 'composite' {
  if ((t.spec?.steps ?? []).length > 0) return 'composite'
  return 'leaf'
}
