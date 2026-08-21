import { defineConfig, devices } from '@playwright/test'

// Playwright config used ONLY for capturing screenshots for step 18's
// report + a floor-level real-browser smoke. NOT part of the Vitest
// suite. Runs against `npm run preview` (production build) so the
// screenshots represent what a user actually loads.
export default defineConfig({
  testDir: './src/test/screenshots',
  outputDir: './screenshots',
  fullyParallel: true,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'off',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'npm run build && npm run preview',
    port: 4173,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
  // Update mode wanted (no baselines checked in). --update-snapshots on the CLI
  // writes them; on regular run we accept new snapshots.
  updateSnapshots: 'all',
  snapshotPathTemplate: './screenshots/{arg}{ext}',
})
