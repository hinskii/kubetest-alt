import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { PageHeader } from '../components/PageHeader'
import { PhaseChip } from '../components/PhaseChip'
import { RunStepsTree, type StepResult } from '../components/RunStepsTree'
import { EmptyState, ErrorState, Loading } from '../components/States'
import { absTime, durationMs } from '../components/TimeCells'

type Run = {
  metadata?: { name?: string; uid?: string; namespace?: string; labels?: Record<string, string> }
  spec?: { testRef?: string; source?: string }
  status?: {
    phase?: string
    message?: string
    queuedAt?: string
    startedAt?: string
    finishedAt?: string
    durationMs?: number
    jobName?: string
    toolExitCode?: number
    steps?: Record<string, StepResult>
    metrics?: Record<string, number>
    counts?: { total?: number; passed?: number; failed?: number; skipped?: number }
    artifactRefs?: Array<{ key: string; bucket?: string; sizeBytes?: number }>
  }
}

export default function RunDetail() {
  const { id = '' } = useParams()
  const q = useQuery({
    queryKey: ['run', id],
    queryFn: () => api.getRun(id),
    refetchInterval: 5_000,
  })
  if (q.isLoading) return <Loading what={`run ${id}`} />
  if (q.isError)
    return (
      <div className="p-6">
        <ErrorState
          title={`Couldn't load run ${id}`}
          detail={(q.error as Error).message}
        />
      </div>
    )
  const r = q.data as unknown as Run
  const name = r.metadata?.name ?? id
  const testRef = r.spec?.testRef ?? '—'
  const source = r.spec?.source ?? '—'
  const s = r.status ?? {}
  const artifacts = s.artifactRefs ?? []
  const metrics = s.metrics ?? {}
  const metricEntries = Object.entries(metrics)
  return (
    <>
      <PageHeader
        eyebrow="run"
        title={<span className="font-mono">{name}</span>}
        backTo={{ to: `/tests/${encodeURIComponent(testRef)}`, label: testRef }}
        meta={
          <>
            {s.phase && <PhaseChip phase={s.phase} size="md" />}
            <Link
              to={`/runs/${encodeURIComponent(id)}/logs`}
              className="text-xs uppercase tracking-wide border border-ink px-2 py-1"
            >
              logs →
            </Link>
          </>
        }
      />
      <div className="p-6 grid grid-cols-1 lg:grid-cols-3 gap-8 min-h-0 overflow-auto">
        <section className="lg:col-span-2 space-y-6">
          <dl className="kv">
            <dt>test</dt>
            <dd>
              <Link to={`/tests/${encodeURIComponent(testRef)}`}>{testRef}</Link>
            </dd>
            <dt>source</dt>
            <dd>{source}</dd>
            <dt>queued</dt>
            <dd>{absTime(s.queuedAt)}</dd>
            <dt>started</dt>
            <dd>{absTime(s.startedAt)}</dd>
            <dt>finished</dt>
            <dd>{absTime(s.finishedAt)}</dd>
            <dt>duration</dt>
            <dd>{durationMs(s.durationMs)}</dd>
            {s.toolExitCode !== undefined && (
              <>
                <dt>tool exit code</dt>
                <dd>{s.toolExitCode}</dd>
              </>
            )}
            {s.jobName && (
              <>
                <dt>k8s job</dt>
                <dd>{s.jobName}</dd>
              </>
            )}
            {s.message && (
              <>
                <dt>message</dt>
                <dd className="whitespace-pre-wrap">{s.message}</dd>
              </>
            )}
          </dl>

          <section>
            <SectionTitle>steps</SectionTitle>
            <RunStepsTree steps={s.steps} />
          </section>

          {s.counts && (
            <section>
              <SectionTitle>counts</SectionTitle>
              <dl className="kv">
                <dt>total</dt>
                <dd>{s.counts.total ?? 0}</dd>
                <dt>passed</dt>
                <dd>{s.counts.passed ?? 0}</dd>
                <dt>failed</dt>
                <dd>{s.counts.failed ?? 0}</dd>
                <dt>skipped</dt>
                <dd>{s.counts.skipped ?? 0}</dd>
              </dl>
            </section>
          )}
        </section>

        <aside className="space-y-6">
          <section>
            <SectionTitle>metrics</SectionTitle>
            {metricEntries.length === 0 ? (
              <EmptyState what="metrics" hint="verdict processor emits these; JMeter/JTL/JUnit-tagged runs will populate this section." />
            ) : (
              <dl className="kv">
                {metricEntries.map(([k, v]) => (
                  <div key={k} className="contents">
                    <dt>{k}</dt>
                    <dd>{typeof v === 'number' ? v : String(v)}</dd>
                  </div>
                ))}
              </dl>
            )}
          </section>
          <section>
            <SectionTitle>artifacts</SectionTitle>
            {artifacts.length === 0 ? (
              <EmptyState what="artifacts" hint="test emitted no scraped files." />
            ) : (
              <table className="dense text-sm">
                <tbody>
                  {artifacts.map((a) => (
                    <tr key={a.key}>
                      <td>
                        <a href={api.artifactUrl(id, a.key)} className="underline">
                          {a.key}
                        </a>
                      </td>
                      <td className="text-xs text-subtle">
                        {a.sizeBytes !== undefined ? `${a.sizeBytes} B` : ''}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>
        </aside>
      </div>
    </>
  )
}

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <h2 className="text-xs text-subtle tracking-wide uppercase mb-2 pb-1 border-b border-rule">
      {children}
    </h2>
  )
}
