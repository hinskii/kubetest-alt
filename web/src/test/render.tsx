import { render, type RenderOptions } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { ReactElement } from 'react'

// Consistent test render: fresh QueryClient per render (no cache
// leakage between tests), MemoryRouter with a configurable initial
// path so views that read useParams still work.
//
//   renderWithProviders(<TestsList />)                     // no params
//   renderWithProviders(<TestDetail />,
//     { path: '/tests/:name', route: '/tests/k6-smoke' })  // params
export function renderWithProviders(
  ui: ReactElement,
  {
    path = '/',
    route = path,
    ...options
  }: RenderOptions & { path?: string; route?: string } = {},
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[route]}>
        <Routes>
          <Route path={path} element={ui} />
          <Route path="*" element={ui} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
    options,
  )
}
