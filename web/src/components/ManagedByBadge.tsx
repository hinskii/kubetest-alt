// ManagedByBadge encodes §7 ownership: a gitops-managed Test is
// read-only in the GUI. Filled dark chip + a padlock glyph (NOT an
// empty square — that reads as a checkbox affordance, which is
// exactly the misread this badge is trying to prevent). aria-label
// carries the same message as the tooltip for screen readers, and
// the visible text stays "GITOPS" for the sighted quick-scan.
export function ManagedByBadge({ value }: { value?: string }) {
  if (value !== 'gitops') return null
  return (
    <span
      role="img"
      aria-label="managed by GitOps"
      title="GitOps-managed: edit the source repo, not this Test."
      className="inline-flex items-center gap-1 px-1.5 py-0.5 text-xs uppercase tracking-wide bg-ink text-bone font-semibold"
    >
      <LockGlyph />
      gitops
    </span>
  )
}

function LockGlyph() {
  return (
    <svg aria-hidden="true" viewBox="0 0 10 10" width="9" height="9" className="stroke-bone" fill="none">
      <path d="M2.5 5 V3.5 a2.5 2.5 0 0 1 5 0 V5" strokeWidth="1" />
      <rect x="1.75" y="5" width="6.5" height="4" fill="currentColor" />
    </svg>
  )
}
