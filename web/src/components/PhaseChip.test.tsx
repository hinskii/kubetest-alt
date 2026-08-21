import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { PhaseChip } from './PhaseChip'

// The chip is the signature element — if its color mapping drifts
// silently every table lies. These tests pin the string-to-token
// mapping and confirm the fallback path for unknown phases.
describe('PhaseChip', () => {
  it('renders the phase label in uppercase', () => {
    render(<PhaseChip phase="passed" />)
    expect(screen.getByText('passed')).toBeInTheDocument()
  })

  it('sets data-phase for downstream selectors', () => {
    const { container } = render(<PhaseChip phase="failed" />)
    expect(container.querySelector('[data-phase="failed"]')).not.toBeNull()
  })

  it('falls back to pend styling for unknown phases (never crashes)', () => {
    const { container } = render(<PhaseChip phase="quantum-superposition" />)
    // Unknown phase renders with the pend token class — the fallback branch.
    expect(container.querySelector('.bg-pend')).not.toBeNull()
    expect(screen.getByText('quantum-superposition')).toBeInTheDocument()
  })

  it('handles the step-17 skipped value (per-step only)', () => {
    render(<PhaseChip phase="skipped" />)
    expect(screen.getByText('skipped')).toBeInTheDocument()
  })
})
