import { useParams, Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { PageHeader } from '../components/PageHeader'
import { ManagedByBadge } from '../components/ManagedByBadge'
import { PhaseChip } from '../components/PhaseChip'
import { EmptyState, ErrorState, Loading } from '../components/States'
import { relativeTime } from '../components/TimeCells'

type TestObj = {
  metadata?: {
    name?: string
    namespace?: string
    labels?: Record<string, string>
  }
  spec?: {
    schedule?: string
    concurrencyPolicy?: string
    use?: string[]
    steps?: Array<{
      name?: string
      condition?: string
      optional?: boolean
      execute?: { tests?: Array<{ name: string; count?: number }> }
    }>
    container?: { image?: string; command?: string[]; args?: string[] }
    config?: Record<string, { type: string; default?: string; enum?: string[] }>
    verdict?: { from?: string; errorRateMax?: string }
  }
  status?: {
    latestRun?: { name?: string; phase?: string; finishedAt?: string }
  }
}

export default function TestDetail() {
  const { name = '' } = useParams()
  const q = useQuery({
    queryKey: ['test', name],
    queryFn: () => api.getTest(name),
  })
  const runs = useQuery({
    queryKey: ['runs', { test: name }],
    queryFn: () => api.listRuns({ test: name, limit: 25 }),
    enabled: Boolean(name),
  })

  if (q.isLoading) return <Loading what={`test ${name}`} />
  if (q.isError)
    return (
      <div className="p-6">
        <ErrorState
          title={`Couldn't load test ${name}`}
          detail={(q.error as Error).message}
        />
      </div>
    )
  const t = q.data as unknown as TestObj
  const managedBy = t.metadata?.labels?.['app.kubernetes.io/managed-by']
  const tool = t.metadata?.labels?.['kubetest.io/tool'] ?? '—'
  const composite = (t.spec?.steps ?? []).length > 0
  const usesTemplate = (t.spec?.use ?? []).length > 0
  const shape: 'leaf' | 'composite' = composite ? 'composite' : 'leaf'
  const latest = t.status?.latestRun
  return (
    <>
      <PageHeader
        eyebrow="test"
        title={<span className="font-mono">{name}</span>}
        backTo={{ to: '/tests', label: 'all tests' }}
        meta={
          <>
            <ManagedByBadge value={managedBy} />
            {latest?.phase && (
              // Header chip needs a label — a naked phase word top-
              // right reads as "the app itself failed" instead of
              // "the last run failed."
              <span className="inline-flex items-baseline gap-2">
                <span className="text-xs text-subtle tracking-wide uppercase">
                  last run
                </span>
                <PhaseChip phase={latest.phase} />
              </span>
            )}
          </>
        }
      />
      <div className="p-6 grid grid-cols-1 lg:grid-cols-3 gap-8 min-h-0 overflow-auto">
        <section className="lg:col-span-2 space-y-6">
          <dl className="kv">
            <dt>namespace</dt>
            <dd>{t.metadata?.namespace ?? '—'}</dd>
            {/* Composite parents have no single tool identity — the
                metric label ("composite") is not a UI value. Skip. */}
            {!composite && (
              <>
                <dt>tool</dt>
                <dd>{tool === 'composite' ? '—' : tool}</dd>
              </>
            )}
            <dt>shape</dt>
            <dd className="uppercase tracking-wide">{shape}</dd>
            <dt>concurrency</dt>
            <dd>{t.spec?.concurrencyPolicy ?? 'Allow'}</dd>
            <dt>schedule</dt>
            <dd>{t.spec?.schedule || '—'}</dd>
            {/* Verdict is a LEAF concept — the wrapper's exit-code +
                optional junit/jtl override. Composite parents inherit
                their verdict from step aggregation, so rendering
                "verdict: exitCode" here misleads. Skip for composite. */}
            {!composite && (
              <>
                <dt>verdict</dt>
                <dd>
                  {t.spec?.verdict?.from
                    ? `${t.spec.verdict.from}${
                        t.spec.verdict.errorRateMax
                          ? ` (errorRateMax=${t.spec.verdict.errorRateMax})`
                          : ''
                      }`
                    : 'exitCode'}
                </dd>
              </>
            )}
          </dl>

          {composite ? (
            <CompositeSteps steps={t.spec?.steps ?? []} />
          ) : usesTemplate ? (
            <TemplateSummary
              use={t.spec?.use ?? []}
              command={t.spec?.container?.command ?? []}
              args={t.spec?.container?.args ?? []}
            />
          ) : (
            <LeafSummary
              image={t.spec?.container?.image}
              command={t.spec?.container?.command ?? []}
              args={t.spec?.container?.args ?? []}
            />
          )}

          <ConfigTable config={t.spec?.config ?? {}} />
        </section>

        <aside>
          <div className="text-xs text-subtle tracking-wide uppercase mb-2">
            recent runs
          </div>
          {composite && (
            <p className="text-xs text-subtle mb-3">
              only this composite's own runs appear here. child TestRuns
              (spawned by each step) show up under their own Test's history.
            </p>
          )}
          {runs.isLoading && <Loading what="runs" />}
          {runs.isError && (
            <ErrorState
              title="Couldn't load runs"
              detail={(runs.error as Error).message}
            />
          )}
          {runs.isSuccess && (runs.data ?? []).length === 0 && (
            <EmptyState what="runs yet" hint="Trigger one from CLI or wait for the schedule." />
          )}
          {runs.isSuccess && (runs.data ?? []).length > 0 && (
            <table
              className="dense text-sm"
              data-testid="recent-runs"
              data-recent-runs-of={name}
            >
              <tbody>
                {runs.data!.map((r) => (
                  <tr key={r.uid} data-run-test-ref={r.testRef}>
                    <td>
                      <Link to={`/runs/${encodeURIComponent(r.uid)}`}>
                        <span className="font-mono">{r.name}</span>
                      </Link>
                    </td>
                    <td>
                      <PhaseChip phase={r.phase} />
                    </td>
                    <td className="text-xs text-subtle">
                      {relativeTime(r.finishedAt ?? r.startedAt ?? r.queuedAt)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </aside>
      </div>
    </>
  )
}

function LeafSummary({
  image,
  command,
  args,
}: {
  image?: string
  command: string[]
  args: string[]
}) {
  return (
    <section>
      <SectionTitle>container</SectionTitle>
      <dl className="kv">
        <dt>image</dt>
        <dd>{image ?? '—'}</dd>
        <dt>command</dt>
        <dd>{command.length ? command.join(' ') : '—'}</dd>
        <dt>args</dt>
        <dd>{args.length ? args.join(' ') : '—'}</dd>
      </dl>
    </section>
  )
}

function TemplateSummary({
  use,
  command,
  args,
}: {
  use: string[]
  command: string[]
  args: string[]
}) {
  return (
    <section>
      <SectionTitle>templates</SectionTitle>
      <ul className="text-sm">
        {use.map((u) => (
          <li key={u} className="border-b border-rule py-1">
            <span className="text-xs text-subtle uppercase tracking-wide">
              use{' '}
            </span>
            <span>{u}</span>
          </li>
        ))}
      </ul>
      {(command.length > 0 || args.length > 0) && (
        <dl className="kv mt-4">
          {command.length > 0 && (
            <>
              <dt>command override</dt>
              <dd>{command.join(' ')}</dd>
            </>
          )}
          {args.length > 0 && (
            <>
              <dt>args override</dt>
              <dd>{args.join(' ')}</dd>
            </>
          )}
        </dl>
      )}
    </section>
  )
}

function CompositeSteps({
  steps,
}: {
  steps: NonNullable<TestObj['spec']>['steps']
}) {
  if (!steps) return null
  return (
    <section>
      <SectionTitle>composite steps</SectionTitle>
      <ol className="border border-rule">
        {steps.map((s, i) => (
          <li key={i} className="px-4 py-3 border-b border-rule last:border-b-0">
            <div className="flex items-baseline gap-3">
              <span className="text-xs text-subtle tracking-wide uppercase">
                step {i}
              </span>
              <span className="font-semibold">{s.name || `step-${i}`}</span>
              {s.optional && (
                <span className="text-xs text-subtle uppercase tracking-wide">
                  optional
                </span>
              )}
              {s.condition && s.condition !== 'passed' && (
                <span className="text-xs text-subtle uppercase tracking-wide">
                  condition:{s.condition}
                </span>
              )}
            </div>
            {s.execute?.tests && (
              <ul className="text-sm mt-1">
                {s.execute.tests.map((r, j) => (
                  <li key={j} className="pl-4 border-l border-rule ml-1 py-0.5">
                    <span aria-hidden="true" className="text-subtle mr-1">
                      ↳
                    </span>
                    <Link to={`/tests/${encodeURIComponent(r.name)}`}>
                      {r.name}
                    </Link>
                    {r.count && r.count > 1 && (
                      <span className="text-subtle text-xs">
                        {' '}
                        ×{r.count}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </li>
        ))}
      </ol>
    </section>
  )
}

function ConfigTable({
  config,
}: {
  config: Record<string, { type: string; default?: string; enum?: string[] }>
}) {
  const entries = Object.entries(config ?? {})
  if (entries.length === 0) return null
  return (
    <section>
      <SectionTitle>config parameters</SectionTitle>
      <table className="dense">
        <thead>
          <tr>
            <th>key</th>
            <th>type</th>
            <th>default</th>
            <th>enum</th>
          </tr>
        </thead>
        <tbody>
          {entries.map(([k, v]) => (
            <tr key={k}>
              <td className="font-semibold">{k}</td>
              <td>{v.type}</td>
              <td>{v.default ?? <em className="text-subtle">required</em>}</td>
              <td>{v.enum?.join(', ') || '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  )
}

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <h2 className="text-xs text-subtle tracking-wide uppercase mb-2 pb-1 border-b border-rule">
      {children}
    </h2>
  )
}
