import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import type { TestForm, ToolTemplate } from "@/api/client";
import { errorsFor, fileNameFor, renamed, sourcesFor } from "@/components/editor/wizard";
import { renderRoute } from "@/test/render";
import { server, viewer } from "@/test/server";

// CodeMirror needs a real browser (Playwright covers it); here a textarea.
vi.mock("@/components/editor/code-editor", () => ({
  default: ({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) => (
    <textarea aria-label={label} value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}));

const k6: ToolTemplate = {
  name: "k6",
  tool: "k6",
  image: "grafana/k6:1.4.0",
  mainParam: "script",
  mainKind: "file",
  params: [
    {
      name: "script",
      type: "string",
      required: false,
      description: "The k6 script, relative to the repository root, e.g. perf/checkout.js.",
    },
    { name: "vus", type: "integer", default: "10", required: false },
  ],
};
const playwright: ToolTemplate = {
  name: "playwright",
  tool: "playwright",
  image: "mcr.microsoft.com/playwright:v1.56.0",
  mainParam: "projectDir",
  mainKind: "directory",
  params: [{ name: "projectDir", type: "string", required: false }],
};

const C = "*/api/v1/clusters/dev";
const page = "/clusters/dev/tests/new?namespace=team-a";

/** api answers the editor's calls and records what was posted. */
function api(preview: (f: TestForm) => { yaml?: string; errors?: string[] } = () => ({})) {
  const posted: { created: TestForm[]; dryRun: boolean[] } = { created: [], dryRun: [] };
  server.use(
    http.get(`${C}/namespaces`, () => HttpResponse.json({ namespaces: ["team-a", "team-b"] })),
    http.get(`${C}/templates`, ({ request }) =>
      HttpResponse.json({
        templates: new URL(request.url).searchParams.get("namespace") === "team-a" ? [k6, playwright] : [],
      }),
    ),
    http.post(`${C}/tests/preview`, async ({ request }) => {
      const f = (await request.json()) as TestForm;
      const p = preview(f);
      return HttpResponse.json({ yaml: p.yaml ?? `name: ${f.name}\n`, errors: p.errors ?? [], warnings: [] });
    }),
    http.post(`${C}/tests`, async ({ request }) => {
      const f = (await request.json()) as TestForm;
      const dryRun = new URL(request.url).searchParams.get("dry-run") === "true";
      posted.created.push(f);
      posted.dryRun.push(dryRun);
      return HttpResponse.json(
        { namespace: f.namespace, name: f.name, dryRun, yaml: "" },
        { status: dryRun ? 200 : 201 },
      );
    }),
  );
  return posted;
}

async function pickTool(name: RegExp) {
  await userEvent.click(await screen.findByRole("combobox", { name: "Tool" }));
  await userEvent.click(await screen.findByRole("option", { name }));
}

describe("the new-test wizard", () => {
  it("creates an inline k6 Test in three screens", async () => {
    const posted = api();
    const { router } = renderRoute(page);
    await userEvent.type(await screen.findByLabelText(/^Name lowercase/), "checkout");
    await pickTool(/^k6$/);
    expect(screen.getByTestId("tool-hint")).toHaveTextContent(
      "grafana/k6:1.4.0 — runs one file: paste it or take it from Git",
    );
    expect(screen.getByRole("radio", { name: "Paste the script" })).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Next" }));

    expect(screen.getByLabelText(/^File name/)).toHaveValue("checkout.js");
    await userEvent.type(await screen.findByLabelText("The script"), "export default function () {{}");
    await userEvent.click(screen.getByRole("button", { name: "Next" }));

    expect(screen.getByLabelText(/^vus/)).toHaveAttribute("placeholder", "10");
    await userEvent.click(screen.getByRole("button", { name: "Create test" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/clusters/dev/tests/team-a/checkout"));
    expect(posted.dryRun).toEqual([false]);
    expect(posted.created[0]).toMatchObject({
      namespace: "team-a",
      name: "checkout",
      template: "k6",
      source: "inline",
      files: [{ path: "checkout.js", content: "export default function () {}" }],
      params: {},
    });
  });

  it("creates a Playwright Test from git: no source choice, git fields only", async () => {
    const posted = api();
    renderRoute(page);
    await userEvent.type(await screen.findByLabelText(/^Name lowercase/), "web");
    await pickTool(/^playwright$/);
    expect(screen.queryByRole("radiogroup")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByText(/playwright runs a project/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(/^Repository/), "https://github.com/acme/web");
    await userEvent.type(screen.getByLabelText(/^Path in the repository/), "e2e");
    await userEvent.click(screen.getByLabelText("Private repository?"));
    await userEvent.type(screen.getByLabelText(/^Secret with the token/), "git-token");
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await userEvent.click(screen.getByRole("button", { name: "Create test" }));
    await waitFor(() => expect(posted.created).toHaveLength(1));
    expect(posted.created[0]).toMatchObject({
      template: "playwright",
      source: "git",
      gitUri: "https://github.com/acme/web",
      gitPath: "e2e",
      gitSecret: "git-token",
      files: [],
    });
  });

  it("builds the YAML as you type and keeps the last good one", async () => {
    api((f) =>
      f.name.includes("!")
        ? { yaml: "", errors: ["Name: not a valid name"] }
        : { yaml: `metadata:\n  name: ${f.name}\n`, errors: f.template ? [] : ["Template: pick a tool"] },
    );
    renderRoute(page);
    const panel = within(await screen.findByRole("region", { name: "YAML" }));
    await userEvent.type(screen.getByLabelText(/^Name lowercase/), "abc");
    await waitFor(() => expect(panel.getByLabelText("The Test's YAML")).toHaveTextContent("name: abc"));
    expect(panel.getByTestId("yaml-missing")).toHaveTextContent("1 to fill in before it can be created");

    await userEvent.type(screen.getByLabelText(/^Name lowercase/), "!");
    await waitFor(() =>
      expect(panel.getByTestId("yaml-missing")).toHaveTextContent("Name: not a valid name"),
    );
    expect(panel.getByLabelText("The Test's YAML")).toHaveTextContent("name: abc");
  });

  it("shows a step's problems beside its fields instead of moving on", async () => {
    api((f) => ({ errors: f.name ? [] : ["Name: required, lowercase letters, digits and -."] }));
    renderRoute(page);
    await screen.findByLabelText(/^Name lowercase/);
    await waitFor(() => expect(screen.getByTestId("yaml-missing")).toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(
      within(screen.getByRole("region", { name: "What do you test?" })).getByText(
        "Name: required, lowercase letters, digits and -.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /1\. What do you test\?/ })).toHaveAttribute(
      "aria-current",
      "step",
    );
  });

  it("checks with a dry run and lists what the API refuses", async () => {
    const posted = api();
    let refuse = false;
    server.use(
      http.post(`${C}/tests`, ({ request }) => {
        posted.dryRun.push(new URL(request.url).searchParams.get("dry-run") === "true");
        return refuse
          ? HttpResponse.json(
              {
                error: "Image: required without a template.",
                errors: ["Image: required without a template."],
              },
              { status: 400 },
            )
          : HttpResponse.json({ namespace: "team-a", name: "x", dryRun: true, yaml: "" });
      }),
    );
    renderRoute(page);
    await userEvent.type(await screen.findByLabelText(/^Name lowercase/), "x");
    await pickTool(/^Own image/);
    await userEvent.click(screen.getByRole("button", { name: /3\. Parameters/ }));
    expect(screen.getByText("An own image has no template parameters.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Check" }));
    expect(await screen.findByRole("status")).toHaveTextContent("The API would admit this Test");
    refuse = true;
    await userEvent.click(screen.getByRole("button", { name: "Create test" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Not created:Image: required without a template.",
    );
    expect(posted.dryRun).toEqual([true, false]);
  });

  it("offers every source for an own image and asks for the image on step 2", async () => {
    api();
    renderRoute(page);
    await pickTool(/^Own image/);
    const sources = within(screen.getByRole("radiogroup"));
    expect(sources.getAllByRole("radio").map((r) => r.getAttribute("value"))).toEqual(["", "git", "inline"]);
    expect(sources.getByRole("radio", { name: /None/ })).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: /2\. Where/ }));
    expect(screen.getByLabelText(/^Image/)).toBeInTheDocument();
    expect(screen.getByLabelText(/^Command/)).toBeInTheDocument();
  });

  it("switches to YAML and marks the server's errors", async () => {
    const posted = api((f) =>
      f.mode === "yaml"
        ? { yaml: "", errors: ["YAML: error converting YAML to JSON: yaml: line 3: bad"] }
        : { yaml: "kind: Test\n" },
    );
    renderRoute(page);
    await screen.findByLabelText(/^Name lowercase/);
    await userEvent.click(screen.getByRole("button", { name: /3\. Parameters/ }));
    await waitFor(() => expect(screen.getByLabelText("The Test's YAML")).toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "Edit as YAML" }));
    await userEvent.click(screen.getByRole("button", { name: "Back to the form" }));
    expect(screen.getByRole("list", { name: "Steps" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit as YAML" }));

    const editor = await screen.findByLabelText("The Test as YAML");
    expect(editor).toHaveValue("kind: Test\n");
    await userEvent.type(editor, "x: [[");
    await waitFor(() => expect(screen.getByTestId("yaml-missing")).toHaveTextContent("line 3"));
    await userEvent.click(screen.getByRole("button", { name: "Create test" }));
    await waitFor(() => expect(posted.created[0]).toMatchObject({ mode: "yaml", yaml: "kind: Test\nx: [" }));
  });

  it("duplicates a Test under a new name", async () => {
    const posted = api();
    server.use(
      http.get(`${C}/tests/team-a/smoke/form`, () =>
        HttpResponse.json({
          locked: true,
          form: {
            namespace: "team-a",
            name: "smoke",
            template: "k6",
            source: "git",
            gitUri: "https://github.com/acme/perf",
            gitPath: "perf/smoke.js",
            resourceVersion: "42",
            mode: "form",
            yaml: "metadata:\n  name: smoke\n",
          },
        }),
      ),
    );
    renderRoute("/clusters/dev/tests/new?namespace=team-a&from=smoke");
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent("Duplicate smoke");
    expect(await screen.findByLabelText(/^Name lowercase/)).toHaveValue("smoke-copy");
    await userEvent.click(screen.getByRole("button", { name: /3\. Parameters/ }));
    await userEvent.click(screen.getByRole("button", { name: "Create test" }));
    await waitFor(() => expect(posted.created).toHaveLength(1));
    expect(posted.created[0]).toMatchObject({
      name: "smoke-copy",
      resourceVersion: "",
      gitPath: "perf/smoke.js",
      yaml: "metadata:\n  name: smoke-copy\n",
    });
  });

  it("tells a viewer creating needs the developer role", async () => {
    server.use(http.get("*/api/v1/me", () => HttpResponse.json(viewer)));
    renderRoute(page);
    expect(await screen.findByRole("status")).toHaveTextContent("Creating a Test needs the developer role");
  });

  it("is linked from the cluster page and a Test's Duplicate", async () => {
    renderRoute("/clusters/dev");
    expect(await screen.findByRole("link", { name: "New test" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/new",
    );
  });
});

describe("wizard rules", () => {
  it("offers the sources a tool can take", () => {
    expect(sourcesFor(k6)).toEqual(["inline", "git"]);
    expect(sourcesFor(playwright)).toEqual(["git"]);
    expect(sourcesFor({ ...k6, mainKind: "" })).toEqual(["", "git"]);
    expect(sourcesFor(undefined)).toEqual(["", "git", "inline"]);
  });

  it("names the script after the tool's example", () => {
    expect(fileNameFor(k6)).toBe("checkout.js");
    expect(fileNameFor(playwright)).toBe("script");
    expect(fileNameFor(undefined)).toBe("script");
  });

  it("finds a field's errors by its label", () => {
    const errs = [
      "Name: bad",
      "Namespace: bad",
      "Command or arguments: at least one",
      "Git repository: required",
    ];
    expect(errorsFor(errs, "Name")).toEqual(["Name: bad"]);
    expect(errorsFor(errs, "Command or arguments")).toEqual(["Command or arguments: at least one"]);
    expect(errorsFor(errs, "Git repository", "Image")).toEqual(["Git repository: required"]);
  });

  it("renames a manifest", () => {
    expect(renamed("kind: Test\nmetadata:\n  name: a.b\n  namespace: x\n", "a.b", "a.b-copy")).toBe(
      "kind: Test\nmetadata:\n  name: a.b-copy\n  namespace: x\n",
    );
    expect(renamed("metadata:\n  name: axb\n", "a.b", "c")).toBe("metadata:\n  name: axb\n");
  });
});
