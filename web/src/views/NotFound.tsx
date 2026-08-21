import { Link } from 'react-router-dom'

export default function NotFound() {
  return (
    <div className="p-8">
      <div className="text-xs text-subtle tracking-wide uppercase mb-1">
        404
      </div>
      <h1 className="text-lg font-semibold mb-4">no such page</h1>
      <p className="text-sm text-subtle">
        This URL doesn't exist. Head to{' '}
        <Link to="/tests" className="underline">tests</Link> or{' '}
        <Link to="/runs" className="underline">runs</Link>.
      </p>
    </div>
  )
}
