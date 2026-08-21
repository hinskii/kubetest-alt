import { useEffect, useMemo, useRef, useState } from 'react'

// LogsViewer state machine — the plan requires EXPLICIT states, not
// a bare spinner. A close after a terminal-phase run is a real end
// state (closed-EOF), not "reconnecting forever."
//
// Transitions:
//   connecting  → replaying (on first frame, if the server tags it as
//                 backfill; step-08 sends the ring buffer first)
//   connecting  → live (if no backfill needed — new run, empty buffer)
//   replaying   → live (on the replay/live boundary sentinel)
//   live        → closed-EOF (on graceful close; the run is terminal)
//   live        → reconnecting (on unexpected close/error; backoff)
//   reconnecting → connecting (retry attempt)
//
// The server signals "replay finished" by an application-level marker
// frame. The step-08 apiserver uses a JSON envelope for control
// messages; a bare string frame is a log line.
export type LogsState =
  | 'connecting'
  | 'replaying'
  | 'live'
  | 'closed-eof'
  | 'reconnecting'

type ControlFrame = { type: 'end-of-replay' } | { type: 'end'; reason?: string }

const MAX_LINES = 5000
const BACKOFF_MS = [500, 1000, 2000, 4000, 8000]

// WsFactory abstracts new WebSocket for tests — vitest supplies a mock
// factory (see LogsViewer.test.tsx) so we don't rely on jsdom's ws.
export type WsFactory = (url: string) => WebSocket

export function LogsViewer({
  url,
  wsFactory,
  maxLines = MAX_LINES,
}: {
  url: string
  wsFactory?: WsFactory
  maxLines?: number
}) {
  const [state, setState] = useState<LogsState>('connecting')
  const [lines, setLines] = useState<string[]>([])
  const [attempt, setAttempt] = useState(0)
  const [pinned, setPinned] = useState(true)
  const wsRef = useRef<WebSocket | null>(null)
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const bytesRef = useRef(0)

  useEffect(() => {
    const factory: WsFactory =
      wsFactory ?? ((u) => new WebSocket(u))
    let cancelled = false
    let timer: number | undefined
    let attemptI = attempt

    function connect() {
      if (cancelled) return
      setState((s) => (s === 'closed-eof' ? s : attemptI === 0 ? 'connecting' : 'reconnecting'))
      const ws = factory(url)
      wsRef.current = ws
      ws.onopen = () => {
        if (cancelled) return
        // Some servers deliver replay before switching to live; assume
        // replaying until an end-of-replay control frame arrives OR the
        // first non-backfilled line lands.
        setState('replaying')
      }
      ws.onmessage = (ev) => {
        if (cancelled) return
        const data = typeof ev.data === 'string' ? ev.data : ''
        bytesRef.current += data.length
        // Control frames are JSON objects with a `type`; anything else
        // is a log line. This mirrors the step-08 log-stream envelope.
        if (data.startsWith('{')) {
          try {
            const c = JSON.parse(data) as ControlFrame
            if (c.type === 'end-of-replay') {
              setState('live')
              return
            }
            if (c.type === 'end') {
              setState('closed-eof')
              ws.close()
              return
            }
          } catch {
            // Not JSON — treat as a log line.
          }
        }
        appendLine(data)
        // The first data frame in a session promotes connecting→live
        // when the server hasn't announced a replay boundary.
        setState((s) => (s === 'connecting' ? 'live' : s))
      }
      ws.onerror = () => {
        // onerror always fires before onclose. Don't react here — let
        // onclose decide EOF vs reconnect.
      }
      ws.onclose = (ev) => {
        if (cancelled) return
        // Code 1000 = normal close (run terminated, server done). Any
        // other code is a network hiccup we retry.
        if (ev.code === 1000 || state === 'closed-eof') {
          setState('closed-eof')
          return
        }
        const delay = BACKOFF_MS[Math.min(attemptI, BACKOFF_MS.length - 1)]
        setState('reconnecting')
        timer = window.setTimeout(() => {
          attemptI += 1
          setAttempt(attemptI)
        }, delay)
      }
    }
    function appendLine(line: string) {
      setLines((prev) => {
        // De-dupe adjacent identical lines to soften a server-side retry
        // that replays the last frame — mirrors the step-08 byte-dedupe
        // in log storage.
        if (prev.length > 0 && prev[prev.length - 1] === line) return prev
        const next = prev.length >= maxLines ? prev.slice(-maxLines + 1) : prev.slice()
        next.push(line)
        return next
      })
    }
    connect()
    return () => {
      cancelled = true
      if (timer) window.clearTimeout(timer)
      wsRef.current?.close()
    }
    // Reconnect on attempt bump; url change means new run entirely.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url, attempt])

  // Auto-scroll when pinned to bottom. Detach pin on user scroll-up.
  useEffect(() => {
    if (!pinned) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines, pinned])

  const isTerminal = state === 'closed-eof'
  const status = useMemo(() => statusFor(state, attempt), [state, attempt])

  return (
    <div className="flex flex-col h-full min-h-0">
      <div className="flex items-center gap-3 px-4 py-2 border-b border-rule bg-bone">
        <StatusIndicator state={state} />
        <span className="text-xs text-subtle uppercase tracking-wide">
          {status}
        </span>
        <span className="ml-auto text-xs text-subtle">
          {lines.length} line{lines.length === 1 ? '' : 's'}
          {isTerminal && ' · run finished'}
        </span>
        {!pinned && (
          <button
            type="button"
            onClick={() => setPinned(true)}
            className="text-xs uppercase tracking-wide border border-ink px-2 py-0.5"
          >
            follow
          </button>
        )}
      </div>
      <div
        ref={scrollRef}
        onScroll={(e) => {
          const el = e.currentTarget
          const atBottom =
            el.scrollHeight - el.scrollTop - el.clientHeight < 8
          setPinned(atBottom)
        }}
        className="logs flex-1 min-h-0"
        aria-label="run logs"
        aria-live={state === 'live' ? 'polite' : 'off'}
      >
        {lines.length === 0 ? (
          <div className="text-subtle text-sm">no output yet</div>
        ) : (
          lines.map((l, i) => <div key={i}>{l}</div>)
        )}
      </div>
    </div>
  )
}

function StatusIndicator({ state }: { state: LogsState }) {
  if (state === 'live') {
    return (
      <span aria-hidden="true" className="live-dot" title="streaming live" />
    )
  }
  if (state === 'closed-eof') {
    return (
      <span aria-hidden="true" className="inline-block w-2 h-2 bg-ink" title="run finished" />
    )
  }
  if (state === 'reconnecting') {
    return (
      <span aria-hidden="true" className="inline-block w-2 h-2 border border-err bg-transparent" title="reconnecting" />
    )
  }
  return (
    <span aria-hidden="true" className="inline-block w-2 h-2 border border-subtle" title={state} />
  )
}

function statusFor(state: LogsState, attempt: number): string {
  switch (state) {
    case 'connecting':
      return 'connecting'
    case 'replaying':
      return 'replaying buffered'
    case 'live':
      return 'live'
    case 'closed-eof':
      return 'closed · EOF'
    case 'reconnecting':
      return `reconnecting (attempt ${attempt + 1})`
  }
}
