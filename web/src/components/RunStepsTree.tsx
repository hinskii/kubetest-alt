import { PhaseChip } from './PhaseChip'
import { absTime, durationMs } from './TimeCells'

// RunStepsTree renders TestRun.status.steps (step 17). The map uses two
// key conventions:
//   - "s{idx}"                  → step aggregate (parent-of-children)
//   - "s{idx}/{testRef}[{i}]"   → single child TestRun in that step
//
// A leaf (non-composite) run produces neither convention — its steps
// map is empty and we render "no steps recorded" rather than a fake
// single-row tree that would misrepresent the shape.
//
// StepPhase includes "skipped" (composite skip-on-fail marker); the
// PhaseChip mapping handles it via the pend/warm-slate color.

export type StepResult = {
  phase?: string
  queuedAt?: string
  startedAt?: string
  finishedAt?: string
  durationMs?: number
}

type ChildRow = {
  key: string       // "s0/leaf-a[0]"
  step: number
  testRef: string
  index: number
  result: StepResult
}

type Group = {
  step: number
  aggregate?: StepResult   // "s{idx}" entry, if any
  children: ChildRow[]
}

const RE_CHILD = /^s(\d+)\/(.+)\[(\d+)\]$/
const RE_AGG = /^s(\d+)$/

// eslint-disable-next-line react-refresh/only-export-components -- pure helper used by both the component and its tests
export function groupSteps(steps: Record<string, StepResult>): Group[] {
  const by: Map<number, Group> = new Map()
  const getOrInit = (idx: number): Group => {
    let g = by.get(idx)
    if (!g) {
      g = { step: idx, children: [] }
      by.set(idx, g)
    }
    return g
  }
  for (const [k, v] of Object.entries(steps ?? {})) {
    const aggM = RE_AGG.exec(k)
    if (aggM) {
      getOrInit(Number(aggM[1])).aggregate = v
      continue
    }
    const childM = RE_CHILD.exec(k)
    if (childM) {
      const [, sIdx, testRef, i] = childM
      getOrInit(Number(sIdx)).children.push({
        key: k,
        step: Number(sIdx),
        testRef: testRef!,
        index: Number(i),
        result: v,
      })
    }
    // Any unrecognized key is a legacy leaf-style entry — skip so the
    // tree stays honest.
  }
  const out = [...by.values()]
  out.sort((a, b) => a.step - b.step)
  for (const g of out) {
    g.children.sort(
      (a, b) =>
        a.testRef.localeCompare(b.testRef) || a.index - b.index,
    )
  }
  return out
}

export function RunStepsTree({ steps }: { steps?: Record<string, StepResult> }) {
  const map = steps ?? {}
  if (Object.keys(map).length === 0) {
    return (
      <div className="text-sm text-subtle">no steps recorded (leaf run)</div>
    )
  }
  const groups = groupSteps(map)
  if (groups.length === 0) {
    // Steps map exists but doesn't match composite key shapes — surface
    // the raw keys so operators aren't gaslit into thinking the tree is
    // rendering correctly.
    return (
      <div className="text-sm text-subtle">
        {Object.keys(map).length} step entr{Object.keys(map).length === 1 ? 'y' : 'ies'} — unrecognized key shape
      </div>
    )
  }
  return (
    <ol className="border border-rule">
      {groups.map((g) => (
        <li key={g.step} className="border-b border-rule last:border-b-0">
          <div className="flex items-baseline gap-4 px-4 py-2 bg-band">
            <span className="text-xs text-subtle tracking-wide uppercase">
              step {g.step}
            </span>
            {g.aggregate?.phase && <PhaseChip phase={g.aggregate.phase} />}
            <span className="ml-auto text-xs text-subtle">
              {durationMs(
                g.aggregate?.durationMs ??
                  msBetween(g.aggregate?.startedAt, g.aggregate?.finishedAt),
              )}
            </span>
          </div>
          {g.children.length === 0 ? (
            <div className="px-4 py-2 text-xs text-subtle">
              step-level aggregate only — no per-child rows recorded
            </div>
          ) : (
            <ul>
              {g.children.map((c) => (
                <li
                  key={c.key}
                  className="grid grid-cols-[24px_1fr_auto_auto] items-baseline gap-4 px-4 py-1.5 border-t border-rule"
                >
                  <span aria-hidden="true" className="text-xs text-subtle">
                    ↳
                  </span>
                  <span className="text-sm">
                    <span>{c.testRef}</span>
                    <span className="text-subtle">[{c.index}]</span>
                  </span>
                  <PhaseChip phase={c.result.phase ?? ''} />
                  <span className="text-xs text-subtle">
                    {absTime(c.result.finishedAt) ?? '—'}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </li>
      ))}
    </ol>
  )
}

function msBetween(a?: string, b?: string): number | undefined {
  if (!a || !b) return undefined
  const av = new Date(a).getTime()
  const bv = new Date(b).getTime()
  if (Number.isNaN(av) || Number.isNaN(bv)) return undefined
  return Math.max(0, bv - av)
}
