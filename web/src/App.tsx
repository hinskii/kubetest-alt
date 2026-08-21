import { Link, Route, Routes, NavLink, Navigate } from 'react-router-dom'
import TestsList from './views/TestsList'
import TestDetail from './views/TestDetail'
import RunsList from './views/RunsList'
import RunDetail from './views/RunDetail'
import LogsView from './views/LogsView'
import NotFound from './views/NotFound'

// The app frame is deliberately narrow: a 240px left rail with two
// primary destinations (tests / runs), a status footer, and the main
// scroll. No breadcrumbs, no header search — those are marketing UX
// concessions we're refusing (tests+runs live at stable URLs, the
// browser's back button and address bar are the wayfinding tools).
export default function App() {
  return (
    <div className="min-h-full grid grid-cols-[240px_1fr]">
      <aside className="border-r border-rule flex flex-col">
        <div className="px-4 py-3 border-b border-rule">
          <Link to="/tests" className="block">
            <div className="text-md font-semibold tracking-normal">kubetest</div>
            <div className="text-xs text-subtle tracking-wide uppercase">
              in-house control
            </div>
          </Link>
        </div>
        <nav className="p-2 flex flex-col gap-0.5 text-sm">
          <NavItem to="/tests">Tests</NavItem>
          <NavItem to="/runs">Runs</NavItem>
        </nav>
        <div className="mt-auto p-3 text-xs text-subtle border-t border-rule">
          <div>read-only · step 18</div>
          <div>mutations land in step 19</div>
        </div>
      </aside>
      <main className="min-w-0 flex flex-col">
        <Routes>
          <Route path="/" element={<Navigate to="/tests" replace />} />
          <Route path="/tests" element={<TestsList />} />
          <Route path="/tests/:name" element={<TestDetail />} />
          <Route path="/runs" element={<RunsList />} />
          <Route path="/runs/:id" element={<RunDetail />} />
          <Route path="/runs/:id/logs" element={<LogsView />} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </main>
    </div>
  )
}

function NavItem({ to, children }: { to: string; children: React.ReactNode }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        [
          'px-3 py-1.5 border-l-2',
          isActive
            ? 'border-l-ink bg-hover font-semibold'
            : 'border-l-transparent hover:bg-hover',
        ].join(' ')
      }
    >
      {children}
    </NavLink>
  )
}
