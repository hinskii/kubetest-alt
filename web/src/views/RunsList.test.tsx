import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '../test/mocks/server'
import { emptyRuns } from '../test/mocks/handlers'
import { renderWithProviders } from '../test/render'
import RunsList from './RunsList'
import { runsFixture } from '../test/fixtures'

describe('RunsList', () => {
  it('renders merged cluster + archive rows in one flat table (no origin column)', async () => {
    renderWithProviders(<RunsList />)
    await waitFor(() =>
      expect(screen.getByText('k6-smoke-abc')).toBeInTheDocument(),
    )
    expect(screen.getByText('jmeter-load-def')).toBeInTheDocument()
    expect(screen.getByText('suite-nightly-xyz')).toBeInTheDocument()
    // Origin (cluster|archive) is a server-side merge detail — MUST
    // NOT appear as a column header. Users can still see it on the
    // run-detail page if they need to.
    const headers = Array.from(document.querySelectorAll('th')).map(
      (h) => h.textContent?.trim().toLowerCase() ?? '',
    )
    expect(headers).not.toContain('origin')
    expect(headers).toContain('name')
    expect(headers).toContain('duration')
  })

  it('duration column uses the short scannable format (14s not 14.04s)', async () => {
    renderWithProviders(<RunsList />)
    await waitFor(() => screen.getByText('k6-smoke-abc'))
    const row = screen.getByText('jmeter-load-def').closest('tr')!
    // 14 035 ms → "14s" per durationShort rules.
    const durationCell = row.querySelectorAll('td')[3]!
    expect(durationCell.textContent?.trim()).toBe('14s')
  })

  it('phase filter narrows the visible rows', async () => {
    renderWithProviders(<RunsList />)
    await waitFor(() => screen.getByText('k6-smoke-abc'))
    await userEvent.selectOptions(
      screen.getByLabelText(/filter by phase/i),
      'failed',
    )
    // Wait for refetch to complete.
    await waitFor(() =>
      expect(screen.queryByText('k6-smoke-abc')).toBeNull(),
    )
    expect(screen.getByText('jmeter-load-def')).toBeInTheDocument()
  })

  it('empty state when the merged stream is empty', async () => {
    server.use(emptyRuns)
    renderWithProviders(<RunsList />)
    await waitFor(() =>
      expect(screen.getByText(/no runs yet/i)).toBeInTheDocument(),
    )
    expect(screen.getByText(/trigger a Test/i)).toBeInTheDocument()
  })

  it('sends the correct query params exactly once per filter change (msw counting)', async () => {
    const seen: string[] = []
    server.use(
      http.get('/api/runs', ({ request }) => {
        seen.push(new URL(request.url).search)
        return HttpResponse.json(runsFixture)
      }),
    )
    renderWithProviders(<RunsList />)
    await waitFor(() => screen.getByText('k6-smoke-abc'))
    await userEvent.selectOptions(
      screen.getByLabelText(/filter by phase/i),
      'passed',
    )
    await waitFor(() => {
      // Fresh query string arrives — exactly one new call per filter change,
      // not per keystroke (react-query dedupes concurrent identical keys).
      expect(seen.filter((s) => s.includes('phase=passed'))).toHaveLength(1)
    })
    // Suppress unused var
    vi.fn()(seen)
  })
})
