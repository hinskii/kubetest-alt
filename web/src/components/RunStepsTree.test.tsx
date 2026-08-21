import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { RunStepsTree, groupSteps, type StepResult } from './RunStepsTree'
import { compositeRun } from '../test/fixtures'

// The RunStepsTree is the composite-run readout. It MUST render the
// scenario 6 shape from test/e2e/e2e_test.go verbatim: three steps,
// step-0 passed, step-1 failed, step-2 skipped.
describe('RunStepsTree', () => {
  const steps = compositeRun.status.steps as unknown as Record<string, StepResult>

  it('groups s{idx} aggregates with their s{idx}/{test}[{i}] children', () => {
    const groups = groupSteps(steps)
    expect(groups).toHaveLength(3)
    expect(groups[0]!.step).toBe(0)
    expect(groups[0]!.aggregate?.phase).toBe('passed')
    expect(groups[0]!.children).toHaveLength(1)
    expect(groups[0]!.children[0]!.testRef).toBe('k6-smoke')
    expect(groups[2]!.aggregate?.phase).toBe('skipped')
  })

  it('renders all three steps + phase chips + skipped marker (§17)', () => {
    render(<RunStepsTree steps={steps} />)
    expect(screen.getByText(/step 0/i)).toBeInTheDocument()
    expect(screen.getByText(/step 1/i)).toBeInTheDocument()
    expect(screen.getByText(/step 2/i)).toBeInTheDocument()
    expect(screen.getByText('skipped')).toBeInTheDocument()
    // The failed child from step 1 must render as a link-like row.
    expect(screen.getByText(/jmeter-load/)).toBeInTheDocument()
  })

  it('leaf run (empty steps map) says so plainly', () => {
    render(<RunStepsTree steps={{}} />)
    expect(screen.getByText(/leaf run/i)).toBeInTheDocument()
  })

  it('unrecognized key shapes surface the count instead of pretending', () => {
    render(<RunStepsTree steps={{ 'random-key': { phase: 'passed' } }} />)
    expect(screen.getByText(/unrecognized key shape/i)).toBeInTheDocument()
  })
})
