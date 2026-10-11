import { expect, test } from "./fixtures";

// The new-test wizard (step 20j acceptance) on the e2e cluster: the k6 and
// playwright templates are applied to the namespace by test/e2e/run.sh.
// The Tests it creates are never run.
const cluster = process.env.CC_CLUSTER ?? "local";
const ns = process.env.E2E_TEST_NS ?? "kubetest-ui-e2e";
const unique = (base: string) => `${base}-${Date.now().toString(36)}`;

test("an inline k6 Test in three screens", async ({ page }) => {
  const name = unique("ui-k6");
  await page.goto(`/clusters/${cluster}/tests/new?namespace=${ns}`);
  // 1. What: name and tool; the script is pasted (the default for k6).
  await page.getByLabel(/^Name lowercase/).fill(name);
  await page.getByRole("combobox", { name: "Tool", exact: true }).click();
  await page.getByRole("option", { name: "k6", exact: true }).click();
  await expect(page.getByRole("radio", { name: "Paste the script" })).toBeChecked();
  await page.getByRole("button", { name: "Next" }).click();
  // 2. Where: paste the script.
  await page.getByLabel("The script").locator(".cm-content").click();
  await page.keyboard.insertText("export default function () {}\n");
  const yaml = page.getByLabel("The Test's YAML");
  await expect(yaml.locator(".cm-content")).toContainText("export default function () {}");
  await page.getByRole("button", { name: "Next" }).click();
  // 3. Parameters: nothing to change; Create.
  await page.getByRole("button", { name: "Create test" }).click();
  await expect(page).toHaveURL(new RegExp(`/clusters/${cluster}/tests/${ns}/${name}$`));
  await expect(page.getByRole("heading", { level: 1 })).toContainText(name);
});

test("a Playwright Test from git", async ({ page }) => {
  const name = unique("ui-pw");
  await page.goto(`/clusters/${cluster}/tests/new?namespace=${ns}`);
  await page.getByLabel(/^Name lowercase/).fill(name);
  await page.getByRole("combobox", { name: "Tool", exact: true }).click();
  await page.getByRole("option", { name: "playwright", exact: true }).click();
  await expect(page.getByRole("radiogroup")).toHaveCount(0);
  await page.getByRole("button", { name: "Next" }).click();
  await page.getByLabel(/^Repository/).fill("https://github.com/microsoft/playwright-examples");
  await page.getByLabel(/^Path in the repository/).fill(".");
  await page.getByRole("button", { name: "Next" }).click();
  await page.getByRole("button", { name: "Create test" }).click();
  await expect(page).toHaveURL(new RegExp(`/clusters/${cluster}/tests/${ns}/${name}$`));
});

test("YAML mode marks the server's error at its line", async ({ page }) => {
  await page.goto(`/clusters/${cluster}/tests/new?namespace=${ns}`);
  await page.getByLabel(/^Name lowercase/).fill(unique("ui-yaml"));
  await page.getByRole("button", { name: /3\. Parameters/ }).click();
  await expect(page.getByLabel("The Test's YAML").locator(".cm-content")).toContainText("kind: Test");
  await page.getByRole("button", { name: "Edit as YAML" }).click();
  const editor = page.getByLabel("The Test as YAML");
  await editor.locator(".cm-content").click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.insertText("foo: bar: baz\n");
  await expect(page.getByTestId("yaml-missing")).toContainText("line");
  await expect(editor.locator(".cm-lint-marker-error")).toHaveCount(1);
});
