import { expect, test } from "./fixtures";

test("home lists the clusters, each reachable", async ({ page }) => {
  await page.goto("/");
  const cards = page.locator("[data-testid^=cluster-]");
  await expect(cards.first()).toBeVisible();
  for (const card of await cards.all()) {
    await expect(card.getByText("connected")).toBeVisible();
  }
  await expect(page.getByTestId("me")).toContainText(process.env.CC_USER ?? "dev@e2e.test");
});

test("the theme toggle switches and is remembered across UIs", async ({ page }) => {
  await page.goto("/");
  const html = page.locator("html");
  const before = await html.getAttribute("data-theme");
  const after = before === "dark" ? "light" : "dark";
  await page.getByRole("button", { name: `Switch to ${after} theme` }).click();
  await expect(html).toHaveAttribute("data-theme", after);
  // A server-rendered page (cluster) reads the same saved choice.
  await page.locator("[data-testid^=cluster-]").first().click();
  await expect(page).toHaveURL(/\/clusters\//);
  await expect(html).toHaveAttribute("data-theme", after);
});
