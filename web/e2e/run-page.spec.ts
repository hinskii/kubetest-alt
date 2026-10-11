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

  // The page follows the run by itself: no reloads. Its stages move on and
  // the log (the first tab) fills.
  await expect(page.getByRole("heading", { level: 1 }).locator("[data-phase=passed]")).toBeVisible({
    timeout: 180_000,
  });
  const stages = page.getByRole("list", { name: "Run stages" }).getByTestId("stage");
  await expect(stages.first()).toHaveAttribute("data-phase", "passed");
  await expect(page.getByTestId("run-log")).toContainText("hello from playwright", { timeout: 30_000 });
  await expect(page.getByRole("link", { name: "Compare with another run" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Abort run" })).toHaveCount(0);

  // Collecting results has its own log.
  await page.getByRole("button", { name: "Show the log of Collect results" }).click();
  await expect(page.getByTestId("run-log")).toContainText("Verdict: passed");

  // The rest is in tabs: the run's parameters under Details.
  await page.getByRole("tab", { name: "Details" }).click();
  const facts = page.getByRole("region", { name: "Facts", exact: true });
  await expect(facts.getByRole("link", { name: "ui-smoke" })).toBeVisible();
  await expect(facts).toContainText("hello from playwright");

  await page.getByRole("button", { name: "Run again" }).click();
  await expect(page.getByText(/Run started again with the parameters of ui-smoke-/)).toBeVisible();
  await expect(page).not.toHaveURL(first);
  await page.getByRole("tab", { name: "Details" }).click();
  await expect(facts).toContainText("hello from playwright");
});
