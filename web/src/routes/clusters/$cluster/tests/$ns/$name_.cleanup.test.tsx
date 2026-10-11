import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import { renderRoute } from "@/test/render";
import { admin, server } from "@/test/server";

const C = "*/api/v1/clusters/dev/tests/team-a/smoke/cleanup";
const page = "/clusters/dev/tests/team-a/smoke/cleanup";
const preview = {
  mode: "older",
  days: 30,
  matches: [
    { id: "old-1", name: "old-1", phase: "passed", finishedAt: "2026-08-01T10:00:00Z", durationMs: 60000 },
    { id: "uid-2", name: "old-2", phase: "failed", finishedAt: "2026-08-20T10:00:00Z", comment: "flake" },
  ],
  scanned: 40,
  max: 200,
};

describe("clean up runs", () => {
  it("previews from the address, oldest first; only admins delete", async () => {
    let asked = "";
    server.use(
      http.get(C, ({ request }) => {
        asked = new URL(request.url).search;
        return HttpResponse.json(preview);
      }),
    );
    renderRoute(`${page}?mode=older&days=30`);
    const matches = within(await screen.findByRole("region", { name: /Matching runs/ }));
    expect(asked).toBe("?mode=older&days=30");
    expect(matches.getByRole("link", { name: "old-2" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/uid-2",
    );
    expect(matches.getByText("flake")).toBeInTheDocument();
    expect(matches.getByRole("checkbox", { name: "delete old-1" })).toBeDisabled();
    expect(matches.getByText("Only admins can delete runs.")).toBeInTheDocument();
  });

  it("sends the form's criteria through the address", async () => {
    server.use(
      http.get(C, () => HttpResponse.json({ ...preview, mode: "range", days: undefined, matches: [] })),
    );
    const { router } = renderRoute(page);
    const form = within(await screen.findByRole("form", { name: "Which runs" }));
    expect(screen.queryByRole("region", { name: /Matching runs/ })).not.toBeInTheDocument();
    await userEvent.click(form.getByLabelText(/Finished between/));
    await userEvent.type(form.getByLabelText("To"), "2026-09-30");
    await userEvent.click(form.getByRole("button", { name: "Preview" }));
    await waitFor(() => expect(router.state.location.search).toEqual({ mode: "range", to: "2026-09-30" }));
    expect(await screen.findByText("No finished runs match.")).toBeInTheDocument();
  });

  it("deletes the checked runs after confirming", async () => {
    let body: unknown;
    server.use(
      http.get("*/api/v1/me", () => HttpResponse.json(admin)),
      http.get(C, () => HttpResponse.json(preview)),
      http.post(C, async ({ request }) => {
        body = await request.json();
        return HttpResponse.json({ deleted: 1, requested: 1, failures: [] });
      }),
    );
    renderRoute(`${page}?mode=older&days=30`);
    const matches = within(await screen.findByRole("region", { name: /Matching runs/ }));
    await userEvent.click(matches.getByRole("checkbox", { name: "delete old-1" }));
    await userEvent.click(matches.getByRole("button", { name: "Delete selected runs" }));
    await userEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete 1 runs" }),
    );
    expect(await screen.findByText("Deleted 1 of 1 runs.")).toBeInTheDocument();
    expect(body).toEqual({ runs: ["uid-2"] });
  });

  it("shows why the criteria were refused", async () => {
    server.use(
      http.get(C, () => HttpResponse.json({ error: "give a From or To date (or both)" }, { status: 400 })),
    );
    renderRoute(`${page}?mode=range`);
    expect(await screen.findByRole("alert")).toHaveTextContent("give a From or To date (or both)");
  });

  it("is linked from the Test page", async () => {
    renderRoute("/clusters/dev/tests/team-a/smoke");
    expect(await screen.findByRole("link", { name: "Clean up runs" })).toHaveAttribute("href", page);
  });
});
