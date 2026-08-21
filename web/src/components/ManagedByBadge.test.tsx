import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ManagedByBadge } from './ManagedByBadge'

describe('ManagedByBadge', () => {
  it('renders nothing for non-gitops values (ui, empty, undefined)', () => {
    const { container: c1 } = render(<ManagedByBadge value="ui" />)
    expect(c1.textContent).toBe('')
    const { container: c2 } = render(<ManagedByBadge value={undefined} />)
    expect(c2.textContent).toBe('')
  })

  it('is an aria-labeled image role, NOT a checkbox affordance', () => {
    render(<ManagedByBadge value="gitops" />)
    const badge = screen.getByRole('img', { name: /managed by gitops/i })
    expect(badge).toBeInTheDocument()
    // The whole point of the redesign: the old empty-square version
    // looked like an unchecked checkbox. The new filled dark chip
    // MUST NOT expose a checkbox role.
    expect(screen.queryByRole('checkbox')).toBeNull()
  })

  it('shows visible GITOPS text for sighted quick-scan', () => {
    render(<ManagedByBadge value="gitops" />)
    expect(screen.getByText(/gitops/i)).toBeInTheDocument()
  })
})
