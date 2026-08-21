import { http, HttpResponse } from 'msw'
import {
  compositeRun,
  compositeTest,
  jmeterTest,
  k6Run,
  k6Test,
  runsFixture,
  testsFixture,
} from '../fixtures'

// Handlers can be overridden per-test via `server.use(...)` — the
// defaults represent a healthy cluster with 3 tests + 3 runs.
export const handlers = [
  http.get('/api/tests', () => HttpResponse.json(testsFixture)),
  http.get('/api/tests/:name', ({ params }) => {
    const name = String(params.name)
    const found = testsFixture.find((t) => t.metadata.name === name)
    if (!found) return HttpResponse.json({ message: 'not found', reason: 'NotFound' }, { status: 404 })
    return HttpResponse.json(found)
  }),
  http.get('/api/runs', ({ request }) => {
    // Mirror the apiserver's server-side filter behavior so tests that
    // change the filter see the runs list actually narrow.
    const q = new URL(request.url).searchParams
    const phase = q.get('phase')
    const test = q.get('test')
    let out = runsFixture
    if (phase) out = out.filter((r) => r.phase === phase)
    if (test) out = out.filter((r) => r.testRef === test)
    return HttpResponse.json(out)
  }),
  http.get('/api/runs/:id', ({ params }) => {
    const id = String(params.id)
    if (id === 'r-3') return HttpResponse.json(compositeRun)
    if (id === 'r-1') return HttpResponse.json(k6Run)
    return HttpResponse.json({ message: 'not found', reason: 'NotFound' }, { status: 404 })
  }),
]

// Named response overrides used by test cases for error/empty state.
export const emptyTests = http.get('/api/tests', () => HttpResponse.json([]))
export const failTests = http.get('/api/tests', () =>
  HttpResponse.json({ message: 'apiserver ate lightning', reason: 'Internal' }, { status: 500 }),
)
export const emptyRuns = http.get('/api/runs', () => HttpResponse.json([]))
export const failRun = http.get('/api/runs/:id', () =>
  HttpResponse.json({ message: 'run vanished', reason: 'NotFound' }, { status: 404 }),
)

// keep k6/jmeter/composite exports for tests that need to compose
export const fixtures = { compositeRun, compositeTest, jmeterTest, k6Run, k6Test, runsFixture, testsFixture }
