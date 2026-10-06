const { defineConfig } = require('cypress');

module.exports = defineConfig({
  reporter: 'junit',
  reporterOptions: { mochaFile: '/data/repo/results/cypress-[hash].xml' },
  video: false,
  e2e: { baseUrl: 'http://target.kubetest-catalog.svc:8000', supportFile: false },
});
