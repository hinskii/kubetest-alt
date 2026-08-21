import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from './mocks/server'

// jsdom doesn't ship WebSocket. Tests that exercise LogsViewer inject
// a custom WsFactory; this global stub only prevents accidental
// `new WebSocket(...)` from throwing during unrelated test bootstrap.
if (typeof globalThis.WebSocket === 'undefined') {
  class NoopWebSocket {
    readyState = 3
    close() {}
    send() {}
    addEventListener() {}
    removeEventListener() {}
    onopen: unknown = null
    onclose: unknown = null
    onerror: unknown = null
    onmessage: unknown = null
  }
  // @ts-expect-error test-only fill-in
  globalThis.WebSocket = NoopWebSocket
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => server.resetHandlers())
afterAll(() => server.close())
