// Loading / Empty / Error primitives. Copy is intentional:
//  - loading uses the verb + object ("loading tests") so it can't be
//    mistaken for a spinner-forever bug.
//  - empty explains what the reader should DO next (an empty screen is
//    an invitation to act — per frontend-design skill).
//  - error says what happened, in the interface's voice, without an
//    apology.
export function Loading({ what }: { what: string }) {
  return (
    <div
      role="status"
      aria-live="polite"
      className="p-6 text-sm text-subtle uppercase tracking-wide"
    >
      loading {what}…
    </div>
  )
}

export function EmptyState({
  what,
  hint,
}: {
  what: string
  hint?: React.ReactNode
}) {
  return (
    <div className="p-8 border border-rule">
      <div className="text-md font-semibold">no {what}</div>
      {hint && <div className="mt-2 text-sm text-subtle">{hint}</div>}
    </div>
  )
}

export function ErrorState({
  title,
  detail,
}: {
  title: string
  detail?: string
}) {
  return (
    <div role="alert" className="p-4 border border-fail text-fail">
      <div className="font-semibold">{title}</div>
      {detail && <div className="text-sm mt-1 text-ink">{detail}</div>}
    </div>
  )
}
