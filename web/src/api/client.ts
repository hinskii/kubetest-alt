// Thin fetch wrapper around the apiserver. Every read path in step 18
// goes through here; step 19 will layer mutations + optimistic updates.
// The URL prefix is `/api` in dev (proxied by vite.config.ts) and same-
// origin in prod (see plan §step-18 auth note).
import type { components } from './generated'

export type Test = components['schemas']['Test']
export type TestRun = components['schemas']['TestRun']
export type RunEnvelope = components['schemas']['RunEnvelope']
export type ApiError = components['schemas']['Error']

const BASE = '/api'

export class HttpError extends Error {
  status: number
  reason?: string
  constructor(status: number, message: string, reason?: string) {
    super(message)
    this.status = status
    this.reason = reason
  }
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.headers ?? {}),
    },
  })
  if (!res.ok) {
    // apiserver always returns Error shape on failure — decode if we can.
    let msg = res.statusText
    let reason: string | undefined
    try {
      const body = (await res.json()) as ApiError
      if (body?.message) msg = body.message
      reason = body?.reason
    } catch {
      // non-JSON error body — keep the statusText message.
    }
    throw new HttpError(res.status, msg, reason)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

// Runs list is a merged { active[], archive[] } envelope — the apiserver
// glues cluster-active TestRuns and archived Postgres rows into one
// stable shape (see internal/apiserver/handlers_runs.go).
export type RunsListParams = {
  limit?: number
  cursor?: string
  test?: string
  phase?: string
  source?: string
}

export const api = {
  listTests: (signal?: AbortSignal) => req<Test[]>('/tests', { signal }),
  getTest: (name: string, signal?: AbortSignal) =>
    req<Test>(`/tests/${encodeURIComponent(name)}`, { signal }),
  listRuns: (params: RunsListParams = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.limit) q.set('limit', String(params.limit))
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.test) q.set('test', params.test)
    if (params.phase) q.set('phase', params.phase)
    if (params.source) q.set('source', params.source)
    const suffix = q.toString() ? `?${q}` : ''
    return req<RunEnvelope[]>(`/runs${suffix}`, { signal })
  },
  getRun: (id: string, signal?: AbortSignal) =>
    req<TestRun>(`/runs/${encodeURIComponent(id)}`, { signal }),
  artifactUrl: (id: string, path: string) =>
    `${BASE}/runs/${encodeURIComponent(id)}/artifacts/${path.split('/').map(encodeURIComponent).join('/')}`,
  // Logs are a WebSocket upgrade — the URL wraps http→ws and hits /api/…
  // through the same-origin path so no CORS + cookies just work.
  logsUrl: (id: string): string => {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${proto}//${window.location.host}${BASE}/runs/${encodeURIComponent(id)}/logs`
  },
}
