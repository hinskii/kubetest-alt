// PhaseChip is the SIGNATURE element: a 4px color bar + uppercase mono
// phase word. Used everywhere phases appear (rows, headers, step trees).
// The bar is the only affordance the eye tracks in a dense table; the
// rest is text-on-paper.
//
// Phase → color mapping (step 17 added "skipped" as a step-only value,
// see api/v1alpha1/common_types.go). Any string outside the known set
// falls back to pend/warm-slate — better than crashing on future values.
import clsx from 'clsx'

export type PhaseValue =
  | 'queued'
  | 'running'
  | 'paused'
  | 'passed'
  | 'failed'
  | 'aborted'
  | 'error'
  | 'skipped'
  | ''
  | string

const COLOR: Record<string, string> = {
  passed: 'bg-pass text-pass',
  failed: 'bg-fail text-fail',
  error: 'bg-err text-err',
  running: 'bg-run text-run',
  queued: 'bg-pend text-pend',
  paused: 'bg-pend text-pend',
  aborted: 'bg-pend text-pend',
  skipped: 'bg-pend text-pend',
}

export function PhaseChip({
  phase,
  size = 'sm',
  className,
}: {
  phase: PhaseValue
  size?: 'sm' | 'md'
  className?: string
}) {
  const label = (phase || 'unknown').toString()
  const style = COLOR[label] ?? 'bg-pend text-pend'
  const [bar, text] = style.split(' ')
  return (
    <span
      className={clsx(
        'inline-flex items-center gap-2 whitespace-nowrap',
        size === 'md' ? 'text-sm' : 'text-xs',
        className,
      )}
      data-phase={label}
    >
      <span
        aria-hidden="true"
        className={clsx('inline-block', bar, size === 'md' ? 'w-1 h-4' : 'w-1 h-3.5')}
      />
      <span className={clsx('uppercase tracking-wide font-semibold', text)}>
        {label}
      </span>
    </span>
  )
}
