// A native <select> styled to match the terminal-on-paper theme. We
// stay on the native element (no custom listbox) so keyboard nav,
// screen readers, and mobile pickers all work for free — but strip
// the OS chrome (default arrow, blue focus outline) with an inline
// caret and the same 2px black focus ring the rest of the app uses.
export function FilterSelect({
  label,
  value,
  onChange,
  options,
  ariaLabel,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  options: readonly string[]
  ariaLabel: string
}) {
  return (
    <label className="text-xs text-subtle tracking-wide uppercase flex items-center gap-2">
      {label}
      <span className="relative inline-block">
        <select
          aria-label={ariaLabel}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="appearance-none border border-rule bg-bone text-ink pl-2 pr-6 py-1 text-sm hover:bg-hover focus:outline-2 focus:outline focus:outline-ink"
        >
          {options.map((o) => (
            <option key={o} value={o}>
              {o || 'all'}
            </option>
          ))}
        </select>
        <span
          aria-hidden="true"
          className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-xs text-subtle"
        >
          ▾
        </span>
      </span>
    </label>
  )
}
