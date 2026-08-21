import { Link } from 'react-router-dom'

// A page header is a title + optional eyebrow + optional right-side
// meta. No hero, no breadcrumbs. Uppercase eyebrow encodes the section
// (like a bylined page in a broadsheet — but here the eyebrow's job is
// wayfinding + tabular density, not decoration).
export function PageHeader({
  eyebrow,
  title,
  backTo,
  meta,
}: {
  eyebrow?: string
  title: React.ReactNode
  backTo?: { to: string; label: string }
  meta?: React.ReactNode
}) {
  return (
    <header className="border-b border-rule px-6 py-4 flex items-baseline gap-6">
      <div className="min-w-0">
        {eyebrow && (
          <div className="text-xs text-subtle tracking-wide uppercase">
            {eyebrow}
          </div>
        )}
        <h1 className="text-lg font-semibold truncate">{title}</h1>
        {backTo && (
          <Link
            to={backTo.to}
            className="text-xs text-subtle tracking-wide uppercase hover:text-ink"
          >
            ← {backTo.label}
          </Link>
        )}
      </div>
      {meta && <div className="ml-auto flex items-center gap-4 text-sm">{meta}</div>}
    </header>
  )
}
