import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { server } from '../test/mocks/server'
import { emptyTests, failTests } from '../test/mocks/handlers'
import { renderWithProviders } from '../test/render'
import TestsList from './TestsList'

describe('TestsList', () => {
  it('shows the loading label first, then the happy-path table', async () => {
    renderWithProviders(<TestsList />)
    expect(screen.getByText(/loading tests/i)).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByText('k6-smoke')).toBeInTheDocument(),
    )
    // GitOps badge visible for gitops-managed rows.
    expect(screen.getAllByText(/gitops/i).length).toBeGreaterThan(0)
    // Composite shape column shows up for the composite Test.
    expect(screen.getByText(/suite-nightly/)).toBeInTheDocument()
  })

  it('renders an empty state with a hint when there are no tests', async () => {
    server.use(emptyTests)
    renderWithProviders(<TestsList />)
    await waitFor(() =>
      expect(screen.getByText(/no tests/i)).toBeInTheDocument(),
    )
    expect(screen.getByText(/apply a Test manifest/i)).toBeInTheDocument()
  })

  it('surfaces an error banner with the apiserver message (no silent failure)', async () => {
    server.use(failTests)
    renderWithProviders(<TestsList />)
    await waitFor(() =>
      expect(
        screen.getByText(/couldn't load tests/i),
      ).toBeInTheDocument(),
    )
    expect(screen.getByText(/apiserver ate lightning/i)).toBeInTheDocument()
  })

  it('filter narrows visible rows and shows a "no matching" message when empty', async () => {
    renderWithProviders(<TestsList />)
    await waitFor(() => screen.getByText('k6-smoke'))
    const input = screen.getByLabelText(/filter tests/i)
    await userEvent.type(input, 'jmeter')
    expect(screen.getByText('jmeter-load')).toBeInTheDocument()
    expect(screen.queryByText('k6-smoke')).toBeNull()
    // Now an unmatchable filter → EmptyState with a "clear the filter" hint.
    await userEvent.clear(input)
    await userEvent.type(input, 'no-such-thing')
    await waitFor(() =>
      expect(screen.getByText(/no matching tests/i)).toBeInTheDocument(),
    )
    expect(screen.getByText(/clear the filter/i)).toBeInTheDocument()
  })

  it('gitops rows are aria-disabled (groundwork for step 19 mutation controls)', async () => {
    renderWithProviders(<TestsList />)
    await waitFor(() => screen.getByText('jmeter-load'))
    const row = screen.getByText('jmeter-load').closest('tr')!
    expect(row).toHaveAttribute('aria-disabled', 'true')
    // Non-gitops row is NOT disabled.
    const row2 = screen.getByText('k6-smoke').closest('tr')!
    expect(row2).toHaveAttribute('aria-disabled', 'false')
  })

  it('composite Test row: TOOL is — (not "composite" — that is a metric label)', async () => {
    renderWithProviders(<TestsList />)
    await waitFor(() => screen.getByText('suite-nightly'))
    const row = screen.getByText('suite-nightly').closest('tr')!
    // TOOL column is the second td.
    const toolCell = row.querySelectorAll('td')[1]!
    expect(toolCell.textContent).toContain('—')
    expect(toolCell.textContent).not.toContain('composite')
    // But the SHAPE column DOES say composite.
    const shapeCell = row.querySelectorAll('td')[3]!
    expect(shapeCell.textContent?.trim().toLowerCase()).toBe('composite')
  })

  it('SHAPE column is binary: leaf | composite (no "template" third value)', async () => {
    renderWithProviders(<TestsList />)
    await waitFor(() => screen.getByText('jmeter-load'))
    // jmeter-load uses spec.use[jmeter] — that's a template delivery,
    // not a shape. Row's SHAPE cell must say "leaf", not "template".
    const row = screen.getByText('jmeter-load').closest('tr')!
    const shapeCell = row.querySelectorAll('td')[3]!
    expect(shapeCell.textContent?.trim().toLowerCase()).toBe('leaf')
    // Template delivery is signaled next to the tool cell instead.
    const toolCell = row.querySelectorAll('td')[1]!
    expect(
      toolCell.querySelector('[aria-label="delivered by template"]'),
    ).not.toBeNull()
  })
})
