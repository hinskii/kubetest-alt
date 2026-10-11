import { expect, test } from "./fixtures";

// Runs the fixture Test (e2e/fixtures/ui-smoke.yaml: busybox echoes the
// greeting) and follows it on its run page.
const cluster = process.env.CC_CLUSTER ?? "local";
const ns = process.env.E2E_TEST_NS ?? "kubetest-ui-e2e";

test("start a run, follow it to the end, run it again", async ({ page }) => {
  test.setTimeout(240_000);
  await page.goto(`/clusters/${cluster}/tests/${ns}/ui-smoke`);
  const form = page.getByRole("form", { name: "Run" });
  await form.getByLabel(/greeting/).fill("hello from playwright");
  await form.getByRole("button", { name: "Run test" }).click();

  await expect(page).toHaveURL(new RegExp(`/clusters/${cluster}/runs/${ns}/ui-smoke-`));
  await expect(page.getByText("Run started.")).toBeVisible();
  const first = page.url();
  const facts = page.getByRole("region", { name: "Facts", exact: true });
  await expect(facts.getByRole("link", { name: "ui-smoke" })).toBeVisible();

  // The page follows the run by itself: no reloads.
  await expect(page.getByRole("heading", { level: 1 }).locator("[data-phase=passed]")).toBeVisible({
    timeout: 180_000,
  });
  await expect(page.getByTestId("run-log")).toContainText("hello from playwright", { timeout: 30_000 });
  await expect(facts).toContainText("hello from playwright"); // the run's parameters
  await expect(page.getByRole("link", { name: "Compare with another run" })).toBeVisible();
  await expect(page.getByRole("form", { name: "Abort" })).toHaveCount(0);

  await page.getByRole("button", { name: "Run again" }).click();
  await expect(page.getByText(/Run started again with the parameters of ui-smoke-/)).toBeVisible();
  await expect(page).not.toHaveURL(first);
  await expect(facts).toContainText("hello from playwright");
});
