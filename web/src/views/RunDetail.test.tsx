import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { server } from '../test/mocks/server'
import { failRun } from '../test/mocks/handlers'
import { renderWithProviders } from '../test/render'
import RunDetail from './RunDetail'

describe('RunDetail', () => {
  it('renders the composite run tree using step-17 keys (scenario 6 shape)', async () => {
    renderWithProviders(<RunDetail />, {
      path: '/runs/:id',
      route: '/runs/r-3',
    })
    await waitFor(() =>
      expect(screen.getByText('suite-nightly-xyz')).toBeInTheDocument(),
    )
    // Composite tree shows three steps + skipped marker.
    expect(screen.getByText(/step 0/i)).toBeInTheDocument()
    expect(screen.getByText(/step 1/i)).toBeInTheDocument()
    expect(screen.getByText(/step 2/i)).toBeInTheDocument()
    // "skipped" appears in the aggregate message copy AND on the chip;
    // scope to the chip via data-phase.
    expect(document.querySelector('[data-phase="skipped"]')).not.toBeNull()
    // Metrics from the failed JTL processor.
    expect(screen.getByText('samples_failed')).toBeInTheDocument()
    // Artifact link.
    expect(
      screen.getByRole('link', { name: 'results/jmeter.jtl' }),
    ).toBeInTheDocument()
  })

  it('leaf run: shows the "no steps" copy, not a fake tree', async () => {
    renderWithProviders(<RunDetail />, {
      path: '/runs/:id',
      route: '/runs/r-1',
    })
    // "k6-smoke-abc" appears both in the page title AND in the back-to-
    // test link — assert on the leaf-run copy specifically instead.
    await waitFor(() =>
      expect(screen.getByText(/leaf run/i)).toBeInTheDocument(),
    )
  })

  it('missing run: 404 surfaces as an ErrorState banner', async () => {
    server.use(failRun)
    renderWithProviders(<RunDetail />, {
      path: '/runs/:id',
      route: '/runs/does-not-exist',
    })
    await waitFor(() =>
      expect(screen.getByRole('alert')).toBeInTheDocument(),
    )
    expect(screen.getByText(/run vanished/i)).toBeInTheDocument()
  })
})
