import { act, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { LogsViewer, type WsFactory } from './LogsViewer'

// A hand-rolled WS mock. jsdom's WebSocket doesn't dispatch events
// synchronously enough to script transitions in a test; the mock gives
// each test full control over open/message/close ordering.
class MockWS {
  static instances: MockWS[] = []
  readyState = 0
  url: string
  onopen: ((ev?: unknown) => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onerror: ((ev?: unknown) => void) | null = null
  onclose: ((ev: { code: number; reason?: string }) => void) | null = null

  constructor(url: string) {
    this.url = url
    MockWS.instances.push(this)
  }
  close() {
    this.readyState = 3
  }
  send() {}
  emitOpen() {
    this.readyState = 1
    this.onopen?.({})
  }
  emitMessage(data: string) {
    this.onmessage?.({ data })
  }
  emitClose(code = 1000) {
    this.readyState = 3
    this.onclose?.({ code })
  }
}

const factory: WsFactory = (u) => new MockWS(u) as unknown as WebSocket

describe('LogsViewer state machine', () => {
  beforeEach(() => {
    MockWS.instances.length = 0
    vi.useFakeTimers()
  })

  it('transitions connecting → replaying → live and renders replayed lines before live', async () => {
    render(<LogsViewer url="ws://x/logs" wsFactory={factory} />)
    // Initial state is "connecting".
    expect(screen.getByText(/^connecting$/i)).toBeInTheDocument()
    const ws = MockWS.instances[0]!
    act(() => ws.emitOpen())
    // onopen promotes to replaying.
    expect(screen.getByText(/replaying/i)).toBeInTheDocument()
    act(() => ws.emitMessage('replayed line 1'))
    act(() => ws.emitMessage('replayed line 2'))
    act(() => ws.emitMessage(JSON.stringify({ type: 'end-of-replay' })))
    expect(screen.getByText(/^live$/i)).toBeInTheDocument()
    act(() => ws.emitMessage('live line 3'))
    // Order preserved: replayed lines rendered before live line — scope
    // the query to the log stream so we don't also match the "N lines"
    // header counter.
    const stream = screen.getByLabelText('run logs')
    const rows = stream.querySelectorAll('div')
    expect(rows[0]!.textContent).toBe('replayed line 1')
    expect(rows[2]!.textContent).toBe('live line 3')
  })

  it('graceful close after terminal run → closed-EOF (no spinner-forever)', () => {
    render(<LogsViewer url="ws://x/logs" wsFactory={factory} />)
    const ws = MockWS.instances[0]!
    act(() => ws.emitOpen())
    act(() => ws.emitMessage(JSON.stringify({ type: 'end-of-replay' })))
    act(() => ws.emitMessage('line'))
    act(() => ws.emitClose(1000))
    expect(screen.getByText(/closed · eof/i)).toBeInTheDocument()
    expect(screen.getByText(/run finished/i)).toBeInTheDocument()
    // MUST NOT show a live pulse dot after EOF.
    expect(document.querySelector('.live-dot')).toBeNull()
  })

  it('abnormal close → reconnecting with attempt counter, no line duplication', () => {
    render(<LogsViewer url="ws://x/logs" wsFactory={factory} />)
    const first = MockWS.instances[0]!
    act(() => first.emitOpen())
    act(() => first.emitMessage('the only line'))
    act(() => first.emitClose(1006))
    expect(screen.getByText(/reconnecting \(attempt 1\)/i)).toBeInTheDocument()
    // Advance backoff (500ms for attempt 0), a new WS is created.
    act(() => {
      vi.advanceTimersByTime(600)
    })
    expect(MockWS.instances).toHaveLength(2)
    const second = MockWS.instances[1]!
    act(() => second.emitOpen())
    // Re-send the same last line — the de-dupe check must drop it.
    act(() => second.emitMessage('the only line'))
    // Only ONE occurrence in the DOM despite the double-delivery.
    const rows = screen.getAllByText('the only line')
    expect(rows).toHaveLength(1)
  })
})
