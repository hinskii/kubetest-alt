import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import type { TestForm } from "@/api/client";
import { itemsToRows, rowsToItems, same } from "@/components/test/settings";
import { renderRoute } from "@/test/render";
import { admin, server, smokeForm, viewer } from "@/test/server";

// CodeMirror needs a real browser (Playwright covers it); here a textarea.
vi.mock("@/components/editor/code-editor", () => ({
  default: ({
    value,
    onChange,
    label,
    readOnly,
  }: {
    value: string;
    onChange: (v: string) => void;
    label: string;
    readOnly?: boolean;
  }) => (
    <textarea
      aria-label={label}
      value={value}
      readOnly={readOnly}
      onChange={(e) => onChange(e.target.value)}
    />
  ),
}));

const T = "*/api/v1/clusters/dev/tests/team-a/smoke";
const page = "/clusters/dev/tests/team-a/smoke?tab=settings";
const section = async (name: string) => within(await screen.findByRole("region", { name }));

/** saves records every PUT; the server then answers the form at version 42. */
function saves(answer?: () => Response) {
  const sent: TestForm[] = [];
  let version = "41";
  server.use(
    http.get(`${T}/form`, () =>
      HttpResponse.json({ ...smokeForm, form: { ...smokeForm.form, resourceVersion: version } }),
    ),
    http.put(T, async ({ request }) => {
      sent.push((await request.json()) as TestForm);
      if (answer) return answer();
      version = "42";
      return HttpResponse.json({ namespace: "team-a", name: "smoke", dryRun: false, yaml: "" });
    }),
  );
  return sent;
}

describe("a Test's settings", () => {
  it("opens from the tab and shows each section filled in", async () => {
    const { router } = renderRoute("/clusters/dev/tests/team-a/smoke");
    await userEvent.click(await screen.findByRole("tab", { name: "Settings" }));
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: "settings" }));
    expect((await section("Source")).getByLabelText(/^Repository/)).toHaveValue(
      "https://github.com/acme/perf",
    );
    expect((await section("Parameters")).getByLabelText(/^vus/)).toHaveValue(20);
    expect((await section("Resources")).getByLabelText("Memory limit")).toHaveValue("1Gi");
    expect((await section("Timeout")).getByLabelText(/^Timeout/)).toHaveValue("15m");
    expect(await (await section("YAML")).findByLabelText("The Test as YAML")).toHaveValue(
      smokeForm.form.yaml,
    );
  });

  it("saves one section alone, at the version shown, and keeps the others' drafts", async () => {
    const sent = saves();
    renderRoute(page);
    const resources = await section("Resources");
    await userEvent.clear(resources.getByLabelText("Memory limit"));
    await userEvent.type(resources.getByLabelText("Memory limit"), "2Gi");
    expect(resources.getByText("Unsaved changes")).toBeInTheDocument();

    const timeout = await section("Timeout");
    await userEvent.clear(timeout.getByLabelText(/^Timeout/));
    await userEvent.type(timeout.getByLabelText(/^Timeout/), "30m");
    await userEvent.click(timeout.getByRole("button", { name: "Save timeout" }));
    expect(await screen.findByText("Timeout saved.")).toBeInTheDocument();
    expect(sent).toEqual([{ ...smokeForm.form, timeout: "30m", mode: "form" }]);
    await waitFor(() => expect(timeout.getByRole("button", { name: "Save timeout" })).toBeDisabled());
    expect(resources.getByLabelText("Memory limit")).toHaveValue("2Gi");
    expect(resources.getByText("Unsaved changes")).toBeInTheDocument();

    await userEvent.click(resources.getByRole("button", { name: "Save resources" }));
    await waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1]).toMatchObject({ memoryLimit: "2Gi", resourceVersion: "42" });
  });

  it("says when the Test changed meanwhile and reloads on request", async () => {
    saves(() =>
      HttpResponse.json(
        { error: "The Test changed since you opened it. Reload it to start from the current version." },
        { status: 409 },
      ),
    );
    renderRoute(page);
    const pod = await section("Pod");
    await userEvent.type(pod.getByLabelText(/^Service account/), "runner");
    await userEvent.click(pod.getByRole("button", { name: "Save pod" }));
    expect(await pod.findByRole("alert")).toHaveTextContent("The Test changed since you opened it.");
    await userEvent.click(pod.getByRole("button", { name: /Reload/ }));
    await waitFor(() => expect(pod.getByLabelText(/^Service account/)).toHaveValue(""));
    expect(pod.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("lists every reason a save is refused", async () => {
    saves(() =>
      HttpResponse.json(
        { error: "x", errors: ['Timeout: "soon" is not a duration like 10m or 1h30m.'] },
        { status: 400 },
      ),
    );
    renderRoute(page);
    const timeout = await section("Timeout");
    await userEvent.clear(timeout.getByLabelText(/^Timeout/));
    await userEvent.type(timeout.getByLabelText(/^Timeout/), "soon");
    await userEvent.click(timeout.getByRole("button", { name: "Save timeout" }));
    expect(await timeout.findByRole("alert")).toHaveTextContent(
      'Not saved:Timeout: "soon" is not a duration like 10m or 1h30m.',
    );
    await userEvent.click(timeout.getByRole("button", { name: "Discard" }));
    expect(timeout.getByLabelText(/^Timeout/)).toHaveValue("15m");
  });

  it("switches the source to files written here, and adds test data as key → path rows", async () => {
    const sent = saves();
    renderRoute(page);
    const source = await section("Source");
    await userEvent.click(source.getByRole("radio", { name: "Files written here" }));
    await userEvent.click(source.getByRole("button", { name: "Add a file" }));
    expect(source.getByLabelText("File 1 name")).toHaveValue("checkout.js");
    await userEvent.type(source.getByLabelText("Content of checkout.js"), "export default function () {{}");
    await userEvent.click(source.getByRole("button", { name: "Save source" }));
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toMatchObject({
      source: "inline",
      files: [{ path: "checkout.js", content: "export default function () {}" }],
    });

    const data = await section("Test data");
    await userEvent.click(data.getByRole("button", { name: "Add test data" }));
    await userEvent.type(data.getByLabelText("Name"), "account");
    await userEvent.click(data.getByRole("combobox", { name: "Where" }));
    await userEvent.click(await screen.findByRole("option", { name: "Some keys as files I choose" }));
    await userEvent.type(data.getByLabelText("Key 1"), "env");
    await userEvent.type(data.getByLabelText("Path 1"), "/data/repo/.env");
    await userEvent.click(data.getByRole("button", { name: "Save test data" }));
    await waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1]!.testData).toEqual([
      { kind: "configMap", name: "account", where: "files", items: "env=/data/repo/.env" },
    ]);
  });

  it("is read-only for a Git-managed Test, with Duplicate", async () => {
    server.use(
      http.get("*/api/v1/me", () => HttpResponse.json(admin)),
      http.get(`${T}/form`, () => HttpResponse.json({ ...smokeForm, locked: true })),
    );
    renderRoute(page);
    expect(await screen.findByRole("link", { name: "duplicate it" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/new?namespace=team-a&from=smoke",
    );
    expect((await section("Timeout")).getByLabelText(/^Timeout/)).toBeDisabled();
    expect(screen.queryByRole("button", { name: /^Save / })).not.toBeInTheDocument();
    expect((await section("YAML")).getByLabelText("The Test as YAML")).toHaveAttribute("readonly");
  });

  it("is read-only for viewers", async () => {
    server.use(http.get("*/api/v1/me", () => HttpResponse.json(viewer)));
    renderRoute(page);
    expect((await section("Pod")).getByLabelText(/^Service account/)).toBeDisabled();
    expect(screen.queryByRole("button", { name: /^Save / })).not.toBeInTheDocument();
  });

  it("edits a Test the form can't show as YAML only", async () => {
    const sent = saves();
    server.use(
      http.get(`${T}/form`, () =>
        HttpResponse.json({ ...smokeForm, yamlOnly: "It has composite steps: edit it as YAML." }),
      ),
    );
    renderRoute(page);
    expect(await screen.findByText("It has composite steps: edit it as YAML.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Source" })).not.toBeInTheDocument();
    const yaml = await section("YAML");
    await userEvent.type(yaml.getByLabelText("The Test as YAML"), "spec: {{}");
    await userEvent.click(yaml.getByRole("button", { name: "Save yaml" }));
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toMatchObject({ mode: "yaml", yaml: `${smokeForm.form.yaml}spec: {}` });
  });
});

describe("settings helpers", () => {
  it("compares fields with empty values alike", () => {
    expect(same(undefined, "")).toBe(true);
    expect(same([], undefined)).toBe(true);
    expect(same({ vus: "" }, {})).toBe(true);
    expect(same({ a: "1", b: "2" }, { b: "2", a: "1" })).toBe(true);
    expect(same("15m", "30m")).toBe(false);
  });

  it("turns test data items into rows and back", () => {
    expect(itemsToRows("env=/data/.env\n\n cfg = /etc/cfg \nbare")).toEqual([
      { key: "env", path: "/data/.env" },
      { key: "cfg", path: "/etc/cfg" },
      { key: "bare", path: "" },
    ]);
    expect(
      rowsToItems([
        { key: "env", path: "/x" },
        { key: "", path: "" },
      ]),
    ).toBe("env=/x");
  });
});
