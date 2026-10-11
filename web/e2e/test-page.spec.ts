import { expect, test } from "./fixtures";

// The fixture Test (e2e/fixtures/ui-smoke.yaml), applied by test/e2e/run.sh.
const cluster = process.env.CC_CLUSTER ?? "local";
const ns = process.env.E2E_TEST_NS ?? "kubetest-ui-e2e";

test("from the tests list to a Test: run form, schedule, definition", async ({ page }) => {
  await page.goto(`/clusters/${cluster}?namespace=${ns}`);
  await expect(page.getByTestId("tests-count")).toContainText("of");
  await page.getByRole("link", { name: "ui-smoke", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/clusters/${cluster}/tests/${ns}/ui-smoke$`));
  await expect(page.getByRole("heading", { level: 1 })).toContainText("ui-smoke");

  const run = page.getByRole("form", { name: "Run" });
  await expect(run.getByLabel(/greeting/)).toHaveValue("hello");
  await expect(run.getByRole("button", { name: "Run test" })).toBeVisible();

  const schedule = page.getByRole("region", { name: "Schedule" });
  await schedule.getByLabel(/Cron/).fill("0 3 * * *");
  await schedule.getByRole("button", { name: "Save schedule" }).click();
  await expect(page.getByText(/Schedule saved\. Next run: .* 03:00:00 UTC\./)).toBeVisible();
  await expect(schedule.getByText("0 3 * * *", { exact: true })).toBeVisible();
  await schedule.getByRole("button", { name: "Remove schedule" }).click();
  await expect(page.getByText("Schedule removed.")).toBeVisible();

  await page.locator("summary", { hasText: "Definition" }).click();
  const yaml = page.getByLabel("Test definition");
  await expect(yaml.locator(".cm-content")).toContainText("image: busybox:1.37");

  // CodeMirror follows the theme (and stays CSP-clean in both).
  const html = page.locator("html");
  const next = (await html.getAttribute("data-theme")) === "dark" ? "light" : "dark";
  await page.getByRole("button", { name: `Switch to ${next} theme` }).click();
  await expect(html).toHaveAttribute("data-theme", next);
  await expect(yaml.locator(".cm-content")).toContainText("image: busybox:1.37");
});
