import { expect, test } from "./fixtures";

// Run flows on the e2e cluster (e2e/fixtures/ui-flows.yaml): a run queued
// behind another (concurrencyPolicy: Forbid) says what it waits for; a
// parallel run's workers fan out, each with its stages and their logs.
const cluster = process.env.CC_CLUSTER ?? "local";
const ns = process.env.E2E_TEST_NS ?? "kubetest-ui-e2e";
const api = `/api/v1/clusters/${cluster}/tests/${ns}`;

test("a run behind another says what it waits for, then runs", async ({ page }) => {
  test.setTimeout(180_000);
  const start = async () => {
    const r = await page.request.post(`${api}/ui-forbid/runs`, { data: { config: {} } });
    expect(r.status(), await r.text()).toBe(201);
    return ((await r.json()) as { name: string }).name;
  };
  // The second starts once the first is running: runs created in the same
  // second may take turns either way.
  const first = await start();
  await expect
    .poll(
      async () => {
        const r = await page.request.get(`/api/v1/clusters/${cluster}/runs/${ns}/${first}`);
        return ((await r.json()) as { phase: string }).phase;
      },
      { timeout: 90_000 },
    )
    .toBe("running");
  const second = await start();

  await page.goto(`/clusters/${cluster}/runs/${ns}/${second}`);
  const queue = page.getByRole("list", { name: "Run stages" }).getByTestId("stage").first();
  await expect(queue).toContainText(`waiting for ${first} to finish (concurrencyPolicy: Forbid)`, {
    timeout: 60_000,
  });
  await expect(queue).toHaveAttribute("data-phase", "running");

  // The Test's run list says it too.
  await page.goto(`/clusters/${cluster}/tests/${ns}/ui-forbid?tab=runs`);
  await expect(page.getByText(`waiting for ${first} to finish`)).toBeVisible();

  // Once the first is done, it runs.
  await page.goto(`/clusters/${cluster}/runs/${ns}/${second}`);
  await expect(page.getByRole("heading", { level: 1 }).locator("[data-phase=passed]")).toBeVisible({
    timeout: 150_000,
  });
});

test("a parallel run's workers fan out, each with its stages and their logs", async ({ page }) => {
  test.setTimeout(180_000);
  const r = await page.request.post(`${api}/ui-parallel/runs`, { data: { config: {} } });
  expect(r.status(), await r.text()).toBe(201);
  const run = ((await r.json()) as { name: string }).name;

  await page.goto(`/clusters/${cluster}/runs/${ns}/${run}`);
  const workers = page.getByRole("region", { name: "Workers" });
  await expect(workers.getByTestId("worker")).toHaveCount(3, { timeout: 60_000 });
  await expect(workers).toContainText("3 of 3 passed", { timeout: 150_000 });
  for (const i of [0, 1, 2]) {
    const stages = workers.getByRole("list", { name: `Worker ${i} stages` }).getByTestId("stage");
    await expect(stages).toHaveCount(4);
    for (const s of await stages.all()) await expect(s).toHaveAttribute("data-phase", "passed");
  }
  await expect(workers).toContainText("greeting=hi");

  const w1 = workers.getByRole("list", { name: "Worker 1 stages" });
  await w1.getByRole("button", { name: /^Show the log of worker 1: Run / }).click();
  await expect(page.getByRole("region", { name: /^Log · Worker 1 · Run / })).toBeVisible();
  await expect(page.getByTestId("run-log")).toContainText("worker 1 of 3 says hi to the e2e");

  await w1.getByRole("button", { name: "Show the log of worker 1: Write files" }).click();
  await expect(page.getByRole("region", { name: "Log · Worker 1 · Write files" })).toBeVisible();
  await expect(page.getByTestId("run-log")).toContainText("Wrote 1 inline file");

  await w1.getByRole("button", { name: "Show the log of worker 1: Collect results" }).click();
  await expect(page.getByRole("region", { name: "Log · Worker 1 · Collect results" })).toBeVisible();
  await expect(page.getByTestId("run-log")).not.toContainText("worker 1 of 3 says");
});
