import { test, expect } from '@playwright/test'
import { fixtures } from '../mocks/handlers'

// Screenshots for the step-18 report. We fake the network via
// Playwright's route interception (no msw needed at the browser
// layer) and capture the four main views: TestsList, TestDetail
// (composite Test), RunsList, RunDetail (composite Run + skipped).

const composite = fixtures.compositeRun
const runsList = fixtures.runsFixture
const compositeTest = fixtures.compositeTest
const k6Test = fixtures.k6Test
const jmeterTest = fixtures.jmeterTest

test.use({ viewport: { width: 1440, height: 900 } })

test.beforeEach(async ({ page }) => {
  await page.route('**/api/tests', (r) =>
    r.fulfill({ contentType: 'application/json', body: JSON.stringify([k6Test, jmeterTest, compositeTest]) }),
  )
  await page.route('**/api/tests/suite-nightly', (r) =>
    r.fulfill({ contentType: 'application/json', body: JSON.stringify(compositeTest) }),
  )
  await page.route('**/api/tests/k6-smoke', (r) =>
    r.fulfill({ contentType: 'application/json', body: JSON.stringify(k6Test) }),
  )
  await page.route('**/api/tests/jmeter-load', (r) =>
    r.fulfill({ contentType: 'application/json', body: JSON.stringify(jmeterTest) }),
  )
  await page.route('**/api/runs**', (r) => {
    // Honor server-side filters so the recent-runs panel on a Test
    // detail page shows only that Test's runs (matches the real
    // apiserver behavior — otherwise the screenshot is a lie).
    const url = new URL(r.request().url())
    const test = url.searchParams.get('test')
    const phase = url.searchParams.get('phase')
    let out = runsList
    if (test) out = out.filter((x) => x.testRef === test)
    if (phase) out = out.filter((x) => x.phase === phase)
    r.fulfill({ contentType: 'application/json', body: JSON.stringify(out) })
  })
  await page.route('**/api/runs/r-3', (r) =>
    r.fulfill({ contentType: 'application/json', body: JSON.stringify(composite) }),
  )
})

test('tests-list.png', async ({ page }) => {
  await page.goto('/tests')
  await page.getByText('k6-smoke').waitFor()
  await expect(page).toHaveScreenshot('tests-list.png', { fullPage: true })
})

test('test-detail-composite.png', async ({ page }) => {
  await page.goto('/tests/suite-nightly')
  await page.getByText('composite steps').waitFor()
  await expect(page).toHaveScreenshot('test-detail-composite.png', { fullPage: true })
})

test('runs-list.png', async ({ page }) => {
  await page.goto('/runs')
  await page.getByText('k6-smoke-abc').waitFor()
  await expect(page).toHaveScreenshot('runs-list.png', { fullPage: true })
})

test('run-detail-composite.png', async ({ page }) => {
  await page.goto('/runs/r-3')
  await page.getByText(/step 0/).waitFor()
  await expect(page).toHaveScreenshot('run-detail-composite.png', { fullPage: true })
})
