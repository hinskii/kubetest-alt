import { describe, expect, it } from 'vitest'
import { durationMs, durationShort, relativeTime } from './TimeCells'

describe('durationShort', () => {
  it('pins the review-required shape: 8s / 14s / 22.4s', () => {
    // The review called out these three values explicitly. Locking
    // them to prevent regression to the ms-heavy format.
    expect(durationShort(8_040)).toBe('8.0s')
    expect(durationShort(14_036)).toBe('14s')
    expect(durationShort(22_400)).toBe('22s')
  })

  it('sub-second values collapse to 0.Ns (Nms is noise at scale)', () => {
    expect(durationShort(120)).toBe('0.1s')
    expect(durationShort(700)).toBe('0.7s')
  })

  it('minute + hour transitions', () => {
    expect(durationShort(90_000)).toBe('1m30s')
    expect(durationShort(3_690_000)).toBe('1h01m')
  })

  it('undefined stays a dash', () => {
    expect(durationShort(undefined)).toBe('—')
  })
})

describe('durationMs (precise; kept for run-detail views)', () => {
  it('still emits fractional-second precision', () => {
    expect(durationMs(8_040)).toBe('8.04s')
  })
})

describe('relativeTime', () => {
  it('produces "Ns ago" for small deltas', () => {
    const now = 1_000_000
    expect(relativeTime(new Date(now - 30_000).toISOString(), now)).toBe('30s ago')
    expect(relativeTime(new Date(now - 5 * 60_000).toISOString(), now)).toBe('5m ago')
  })
})
