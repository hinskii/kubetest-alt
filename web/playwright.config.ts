import { defineConfig, devices } from "@playwright/test";

// Browser tests against a running Control Center: `make dev-ui` (default)
// or the e2e cluster's (test/e2e/run.sh sets CC_URL). CC_URL reaches
// Control Center past the sign-in proxy, so the sign-in header is the
// user, as oauth2-proxy would set it.
export default defineConfig({
  testDir: "./e2e",
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: process.env.CC_URL ?? "http://localhost:8090",
    extraHTTPHeaders: { "X-Auth-Request-Email": process.env.CC_USER ?? "dev@e2e.test" },
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
