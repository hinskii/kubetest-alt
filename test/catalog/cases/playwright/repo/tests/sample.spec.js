const { test, expect } = require('@playwright/test');

test('serves the page', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('h1')).toHaveText('ok');
});

test('fails on purpose', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('h1')).toHaveText('not ok', { timeout: 1000 });
});
