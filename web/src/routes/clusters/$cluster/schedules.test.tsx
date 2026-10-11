import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import { renderRoute } from "@/test/render";
import { server, viewer } from "@/test/server";

describe("schedules", () => {
  it("lists recurring schedules with next fires, and one-time runs", async () => {
    renderRoute("/clusters/dev/schedules");
    const recurring = within(await screen.findByRole("region", { name: /Recurring/ }));
    expect(recurring.getByRole("link", { name: "nightly" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/team-a/nightly",
    );
    expect(recurring.getByText("2026-10-08 02:00:00 UTC")).toBeInTheDocument();
    expect(recurring.getByText(/end of range/)).toBeInTheDocument();
    expect(recurring.getByText("read-only")).toBeInTheDocument();
    const oneTime = within(screen.getByRole("region", { name: /One-time/ }));
    expect(oneTime.getByText("2030-01-02 03:04:00 UTC")).toBeInTheDocument();
    expect(oneTime.getByRole("link", { name: "smoke-later" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-later",
    );
  });

  it("cancels a waiting run", async () => {
    let body: unknown;
    server.use(
      http.post("*/api/v1/clusters/dev/runs/team-a/smoke-later/abort", async ({ request }) => {
        body = await request.json();
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderRoute("/clusters/dev/schedules");
    await userEvent.click(await screen.findByRole("button", { name: "Cancel smoke-later" }));
    expect(await screen.findByText("Run cancelled.")).toBeInTheDocument();
    expect(body).toEqual({ message: "cancelled before its start time" });
  });

  it("gives viewers no cancel", async () => {
    server.use(http.get("*/api/v1/me", () => HttpResponse.json(viewer)));
    renderRoute("/clusters/dev/schedules");
    await screen.findByRole("region", { name: /One-time/ });
    expect(screen.queryByRole("button", { name: /Cancel/ })).not.toBeInTheDocument();
  });

  it("is linked from the tests list", async () => {
    renderRoute("/clusters/dev");
    expect(await screen.findByRole("link", { name: "Schedules" })).toHaveAttribute(
      "href",
      "/clusters/dev/schedules",
    );
  });
});
