import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { renderWithProviders } from '../test/render'
import TestDetail from './TestDetail'

describe('TestDetail', () => {
  it('renders composite steps + linked child Tests', async () => {
    renderWithProviders(<TestDetail />, {
      path: '/tests/:name',
      route: '/tests/suite-nightly',
    })
    await waitFor(() =>
      expect(screen.getByText('composite steps')).toBeInTheDocument(),
    )
    // All three defined step names — scope by matching the bold step
    // header text exactly (avoid matching "smoke" inside test-ref names).
    expect(
      screen.getByText((c, el) => el?.tagName === 'SPAN' && el.classList.contains('font-semibold') && c === 'smoke'),
    ).toBeInTheDocument()
    expect(
      screen.getByText((c, el) => el?.tagName === 'SPAN' && el.classList.contains('font-semibold') && c === 'load'),
    ).toBeInTheDocument()
    expect(
      screen.getByText((c, el) => el?.tagName === 'SPAN' && el.classList.contains('font-semibold') && c === 'cleanup'),
    ).toBeInTheDocument()
    // Optional + condition tags.
    expect(screen.getByText(/optional/i)).toBeInTheDocument()
    expect(screen.getByText(/condition:always/i)).toBeInTheDocument()
    // Child Test names link to their own pages. Multiple k6-smoke
    // links exist (composite step 0 AND step 2 both reference it); each
    // one must resolve to /tests/k6-smoke.
    const links = screen.getAllByRole('link', { name: 'k6-smoke' })
    expect(links.length).toBeGreaterThanOrEqual(1)
    for (const l of links) {
      expect(l.getAttribute('href')).toBe('/tests/k6-smoke')
    }
  })

  it('leaf Test: shows image/command block, not composite steps', async () => {
    renderWithProviders(<TestDetail />, {
      path: '/tests/:name',
      route: '/tests/k6-smoke',
    })
    await waitFor(() =>
      expect(screen.getByText('container')).toBeInTheDocument(),
    )
    expect(screen.queryByText(/composite steps/i)).toBeNull()
    expect(screen.getByText('grafana/k6:1.4.0')).toBeInTheDocument()
  })

  it('template Test: shows use[] block + config table', async () => {
    renderWithProviders(<TestDetail />, {
      path: '/tests/:name',
      route: '/tests/jmeter-load',
    })
    await waitFor(() =>
      expect(screen.getByText('templates')).toBeInTheDocument(),
    )
    // ConfigTable rendered for the `plan` param.
    expect(screen.getByText('config parameters')).toBeInTheDocument()
    expect(screen.getByText('plan')).toBeInTheDocument()
  })

  it('composite Test: no VERDICT KV row (verdict is a leaf concept)', async () => {
    renderWithProviders(<TestDetail />, {
      path: '/tests/:name',
      route: '/tests/suite-nightly',
    })
    await waitFor(() =>
      expect(screen.getByText('composite steps')).toBeInTheDocument(),
    )
    // Composite parents inherit their verdict from step aggregation;
    // rendering "verdict: exitCode" here misleads.
    const kv = document.querySelector('dl.kv')!
    expect(kv.textContent?.toLowerCase()).not.toContain('verdict')
    // TOOL row also omitted for composite — same reason (composite is
    // a metric label, not a UI value).
    expect(kv.textContent?.toLowerCase()).not.toContain('tool')
  })

  it('leaf Test: DOES render the verdict KV row (default exitCode)', async () => {
    renderWithProviders(<TestDetail />, {
      path: '/tests/:name',
      route: '/tests/k6-smoke',
    })
    await waitFor(() => expect(screen.getByText('container')).toBeInTheDocument())
    const kv = document.querySelector('dl.kv')!
    expect(kv.textContent?.toLowerCase()).toContain('verdict')
  })

  it('recent-runs panel filters to THIS test only', async () => {
    renderWithProviders(<TestDetail />, {
      path: '/tests/:name',
      route: '/tests/suite-nightly',
    })
    // Wait for the recent-runs table to render, THEN inspect the rows.
    await waitFor(() =>
      expect(document.querySelector('[data-testid="recent-runs"]')).not.toBeNull(),
    )
    const table = document.querySelector('[data-testid="recent-runs"]')!
    expect(table.getAttribute('data-recent-runs-of')).toBe('suite-nightly')
    const rows = table.querySelectorAll('tr')
    // Only suite-nightly runs — 1 in fixtures. Not the k6 or jmeter
    // runs (those have testRef ≠ suite-nightly). This is the panel-
    // filter regression the design review flagged.
    expect(rows.length).toBe(1)
    for (const r of rows) {
      expect(r.getAttribute('data-run-test-ref')).toBe('suite-nightly')
    }
  })
})
