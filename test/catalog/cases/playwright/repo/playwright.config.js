// @ts-check
const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './tests',
  retries: 0,
  use: { baseURL: 'http://target.kubetest-catalog.svc:8000' },
});
