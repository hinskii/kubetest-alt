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

  await page.getByRole("tab", { name: "Settings" }).click();
  const schedule = page.getByRole("region", { name: "Schedule" });
  await schedule.getByLabel(/Cron/).fill("0 3 * * *");
  await schedule.getByRole("button", { name: "Save schedule" }).click();
  await expect(page.getByText(/Schedule saved\. Next run: .* 03:00:00 UTC\./)).toBeVisible();
  await expect(schedule.getByText("0 3 * * *", { exact: true })).toBeVisible();
  await schedule.getByRole("button", { name: "Remove schedule" }).click();
  await expect(page.getByText("Schedule removed.")).toBeVisible();

  await page.getByRole("tab", { name: "Overview" }).click();
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

test("Settings: a section saves alone", async ({ page }) => {
  await page.goto(`/clusters/${cluster}/tests/${ns}/ui-smoke?tab=settings`);
  const timeout = page.getByRole("region", { name: "Timeout" });
  const field = timeout.getByLabel(/^Timeout/);
  await field.fill("7m");
  await timeout.getByRole("button", { name: "Save timeout" }).click();
  await expect(page.getByText("Timeout saved.")).toBeVisible();
  await expect(page.getByRole("region", { name: "The Test now" }).locator(".cm-content")).toContainText(
    "timeout: 7m0s",
  );

  // Put it back, so the next run starts from the fixture.
  await field.fill("");
  await timeout.getByRole("button", { name: "Save timeout" }).click();
  await expect(page.getByRole("region", { name: "The Test now" }).locator(".cm-content")).not.toContainText(
    "timeout:",
  );
});

test("the tests list: pass rate, recent runs, search and status", async ({ page }) => {
  test.setTimeout(120_000);
  // A finished run of ui-smoke, so it has stats and a recent run.
  const r = await page.request.post(`/api/v1/clusters/${cluster}/tests/${ns}/ui-smoke/runs`, {
    data: { config: {} },
  });
  expect(r.status(), await r.text()).toBe(201);
  const run = ((await r.json()) as { name: string }).name;
  await expect
    .poll(
      async () =>
        (
          (await (await page.request.get(`/api/v1/clusters/${cluster}/runs/${ns}/${run}`)).json()) as {
            finished: boolean;
          }
        ).finished,
      { timeout: 90_000 },
    )
    .toBe(true);

  await page.goto(`/clusters/${cluster}?namespace=${ns}`);
  await page.getByRole("searchbox", { name: "Search" }).fill("ui-smo");
  await expect(page).toHaveURL(/q=ui-smo/);
  const row = page.getByTestId("test-row").filter({ hasText: "ui-smoke" });
  await expect(page.getByTestId("test-row")).toHaveCount(1);
  await expect(row.getByText(/^\d+%$/)).toBeVisible();
  await expect(
    row.getByRole("list", { name: "Recent runs" }).getByRole("link", { name: new RegExp(`^${run} · `) }),
  ).toBeVisible();

  await page.getByRole("combobox", { name: "Status" }).click();
  await page.getByRole("option", { name: "Never run" }).click();
  await expect(page).toHaveURL(/status=never/);
  await expect(page.getByTestId("test-row")).toHaveCount(0);
  await expect(page.getByText("No tests match.")).toBeVisible();
});
