import { expect, test } from "./fixtures";

// Step 20R R3 pages in a real browser (console errors fail the test):
// from a Test to its analytics, test cases and cleanup preview; the
// cluster's schedules.
const cluster = process.env.CC_CLUSTER ?? "local";
const ns = process.env.E2E_TEST_NS ?? "kubetest-ui-e2e";
const test_ = `/clusters/${cluster}/tests/${ns}/ui-smoke`;

test("a Test's insights: the tab, analytics, test cases, clean-up preview", async ({ page }) => {
  await page.goto(test_);
  await expect(page.getByTestId("overview-tile")).toHaveCount(4);
  await page.getByRole("tab", { name: "Insights" }).click();
  await expect(page.getByRole("region", { name: /Last 30 runs/ })).toBeVisible();
  await page.getByRole("link", { name: "Analytics & comparison" }).click();
  await expect(page).toHaveURL(new RegExp(`${test_}/analytics$`));
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("ui-smoke · analytics");
  await expect(page.getByRole("combobox", { name: "Window" })).toBeVisible();

  await page.goto(`${test_}?tab=insights`);
  await page.getByRole("link", { name: "Test cases & flaky tests" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("ui-smoke · test cases");

  await page.goto(`${test_}?tab=runs`);
  await page.getByRole("link", { name: "Clean up runs" }).click();
  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page).toHaveURL(/mode=older/);
  await expect(page.getByRole("region", { name: /Matching runs/ })).toBeVisible();
});

test("the cluster's schedules", async ({ page }) => {
  await page.goto(`/clusters/${cluster}`);
  await page.getByRole("link", { name: "Schedules" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Schedules");
  await expect(page.getByRole("region", { name: /Recurring/ })).toBeVisible();
  await expect(page.getByRole("region", { name: /One-time/ })).toBeVisible();
});
