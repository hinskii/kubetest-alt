import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import { renderRoute } from "@/test/render";
import { server, viewer } from "@/test/server";

describe("tests list", () => {
  it("groups by tool (unlabelled under other), with last run, chips and the count", async () => {
    renderRoute("/clusters/dev");
    expect(await screen.findByTestId("tests-count")).toHaveTextContent("3 of 3 tests");
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Development");
    const groups = screen.getAllByTestId(/^group-/).map((g) => g.dataset.testid);
    expect(groups).toEqual(["group-cypress", "group-k6", "group-other"]);

    const k6 = within(screen.getByTestId("group-k6"));
    expect(k6.getByRole("link", { name: "smoke" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/team-a/smoke",
    );
    expect(k6.getByText("failed").closest("a")).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde",
    );
    expect(k6.getByText("2026-10-07 12:00:00 UTC")).toBeInTheDocument();
    const cypress = within(screen.getByTestId("group-cypress"));
    expect(cypress.getByText("not ready")).toBeInTheDocument();
    expect(cypress.getByText("read-only")).toBeInTheDocument();
    expect(within(screen.getByTestId("group-other")).getByText("never run")).toBeInTheDocument();
  });

  it("filters by namespace and tool from the address", async () => {
    renderRoute("/clusters/dev?namespace=team-b&tool=cypress");
    expect(await screen.findByTestId("tests-count")).toHaveTextContent("1 of 3 tests");
    expect(screen.queryByTestId("group-k6")).not.toBeInTheDocument();
  });

  it("changes the filter in the address", async () => {
    const { router } = renderRoute("/clusters/dev");
    await screen.findByTestId("tests-count");
    await userEvent.click(screen.getByRole("combobox", { name: "Tool" }));
    await userEvent.click(await screen.findByRole("option", { name: "k6" }));
    expect(await screen.findByTestId("tests-count")).toHaveTextContent("1 of 3 tests");
    expect(router.state.location.search).toEqual({ tool: "k6" });
  });

  it("offers New test to developers only", async () => {
    renderRoute("/clusters/dev?namespace=team-a");
    expect(await screen.findByRole("link", { name: "New test" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/new?namespace=team-a",
    );
    expect(screen.getByRole("link", { name: "Schedules" })).toHaveAttribute(
      "href",
      "/clusters/dev/schedules",
    );
  });

  it("hides New test from viewers", async () => {
    server.use(http.get("*/api/v1/me", () => HttpResponse.json(viewer)));
    renderRoute("/clusters/dev");
    await screen.findByTestId("tests-count");
    expect(screen.queryByRole("link", { name: "New test" })).not.toBeInTheDocument();
  });

  it("shows the API's error", async () => {
    server.use(
      http.get("*/api/v1/clusters/dev/tests", () =>
        HttpResponse.json({ error: "Cannot reach the kubetest API of dev." }, { status: 502 }),
      ),
    );
    renderRoute("/clusters/dev");
    expect(await screen.findByRole("alert")).toHaveTextContent("Cannot reach the kubetest API of dev.");
  });
});
