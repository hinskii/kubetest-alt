// ManagedByBadge encodes §7 ownership: a gitops-managed Test is
// read-only in the GUI (mutation controls will be aria-disabled in
// step 19). The badge doesn't decorate — it warns that clicking the
// Run/Edit/Delete buttons wouldn't work here even if step 19 landed.
export function ManagedByBadge({ value }: { value?: string }) {
  if (value !== 'gitops') return null
  return (
    <span
      title="GitOps-managed: edit the source repo, not this Test."
      className="inline-flex items-center gap-1 text-xs text-subtle uppercase tracking-wide"
    >
      <span aria-hidden="true" className="inline-block w-3 h-3 border border-ink" />
      gitops
    </span>
  )
}
