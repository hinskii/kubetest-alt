import { render, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import axe from 'axe-core'

import App from '../App'

// axe smoke: navigate through the three main top-level views and
// assert zero SERIOUS violations. Not a full a11y audit — the
// production audit is a step-20 job — but a floor that catches the
// low-hanging fruit (missing labels, contrast, aria-role misuse).
describe('a11y smoke', () => {
  it.each([['/tests'], ['/runs'], ['/tests/suite-nightly'], ['/runs/r-3']])(
    '%s has no serious axe violations',
    async (route) => {
      const client = new QueryClient({
        defaultOptions: { queries: { retry: false, gcTime: 0 } },
      })
      const { container, unmount } = render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={[route]}>
            <App />
          </MemoryRouter>
        </QueryClientProvider>,
      )
      // Wait for the network-driven views to settle.
      await waitFor(() => {
        expect(container.querySelector('main')?.textContent?.length ?? 0).toBeGreaterThan(20)
      })
      const result = await axe.run(container, {
        rules: {
          // Contrast requires a browser rendering engine; jsdom always
          // fails it. The Playwright screenshot suite covers this in a
          // real browser (see src/test/screenshots/).
          'color-contrast': { enabled: false },
        },
      })
      const serious = result.violations.filter(
        (v) => v.impact === 'critical' || v.impact === 'serious',
      )
      if (serious.length > 0) {
        // Pretty print for the diagnostic — one line per violation.
        console.error(
          serious.map((v) => `${v.id}: ${v.help} @ ${v.nodes.length} node(s)`).join('\n'),
        )
      }
      expect(serious).toEqual([])
      unmount()
    },
    10_000,
  )
})
