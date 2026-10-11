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

  it("shows each Test's pass rate, P50 and recent runs, each a link", async () => {
    renderRoute("/clusters/dev");
    const k6 = within(await screen.findByTestId("group-k6"));
    expect(k6.getByRole("columnheader", { name: "Pass rate (7d)" })).toBeInTheDocument();
    const row = within(k6.getByTestId("test-row"));
    expect(row.getByText("75%")).toHaveClass("text-danger");
    expect(row.getByText("of 8")).toBeInTheDocument();
    expect(row.getByText("5.0s")).toBeInTheDocument();
    const marks = within(row.getByRole("list", { name: "Recent runs" })).getAllByRole("link");
    expect(marks.map((m) => m.dataset.phase)).toEqual(["passed", "failed"]);
    expect(marks[1]).toHaveAttribute("href", "/clusters/dev/runs/team-a/smoke-abcde");
    expect(marks[1]).toHaveAccessibleName("smoke-abcde · failed · 6.0s · 2026-10-07 12:00:00 UTC");
    // No runs: dashes, not zeros.
    const bare = within(within(screen.getByTestId("group-other")).getByTestId("test-row"));
    expect(bare.getAllByText("—")).toHaveLength(3);
  });

  it("searches by name and filters by the last run's status, in the address", async () => {
    const { router } = renderRoute("/clusters/dev");
    await screen.findByTestId("tests-count");
    await userEvent.type(screen.getByRole("searchbox", { name: "Search" }), "SMO");
    expect(await screen.findByTestId("tests-count")).toHaveTextContent("1 of 3 tests");
    expect(router.state.location.search).toEqual({ q: "SMO" });

    await userEvent.clear(screen.getByRole("searchbox", { name: "Search" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Status" }));
    await userEvent.click(await screen.findByRole("option", { name: "Last run passed" }));
    expect(await screen.findByTestId("tests-count")).toHaveTextContent("1 of 3 tests");
    expect(screen.getByTestId("group-cypress")).toBeInTheDocument();
    expect(router.state.location.search).toEqual({ status: "passed" });
  });

  it.each([
    ["failed", ["group-k6"]],
    ["never", ["group-other"]],
  ])("status=%s keeps only those Tests", async (status, groups) => {
    renderRoute(`/clusters/dev?status=${status}`);
    expect(await screen.findByTestId("tests-count")).toHaveTextContent("1 of 3 tests");
    expect(screen.getAllByTestId(/^group-/).map((g) => g.dataset.testid)).toEqual(groups);
  });

  it("sorts by a column: ascending, descending, then back to by name", async () => {
    server.use(
      http.get("*/api/v1/clusters/dev/tests", () =>
        HttpResponse.json({
          range: "7d",
          tests: ["a", "b", "c"].map((name, i) => ({
            name,
            namespace: "team-a",
            tool: "k6",
            locked: false,
            stats:
              name === "c"
                ? undefined
                : {
                    total: 2,
                    passed: 1 + i,
                    failed: 1 - i,
                    errored: 0,
                    aborted: 0,
                    passRatio: [0.5, 1][i],
                    p50Ms: 1000,
                    p95Ms: 1000,
                    recent: [],
                  },
          })),
        }),
      ),
    );
    const { router } = renderRoute("/clusters/dev");
    const names = async () =>
      within(await screen.findByTestId("group-k6"))
        .getAllByTestId("test-row")
        .map((r) => within(r).getAllByRole("link")[0]!.textContent);
    expect(await names()).toEqual(["a", "b", "c"]);
    const header = () => screen.getByRole("button", { name: "Pass rate (7d)" });
    await userEvent.click(header());
    expect(router.state.location.search).toEqual({ sort: "passRate" });
    expect(await names()).toEqual(["a", "b", "c"]);
    await userEvent.click(header());
    expect(router.state.location.search).toEqual({ sort: "passRate", desc: true });
    expect(await names(), "without a rate: last either way").toEqual(["b", "a", "c"]);
    expect(screen.getByRole("columnheader", { name: "Pass rate (7d)" })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    await userEvent.click(header());
    expect(router.state.location.search).toEqual({});
  });

  it("says why there are no stats when the cluster keeps no history", async () => {
    server.use(
      http.get("*/api/v1/clusters/dev/tests", () =>
        HttpResponse.json({
          range: "7d",
          statsUnavailable: "run history store is not configured",
          tests: [{ name: "bare", namespace: "team-b", tool: "", locked: false }],
        }),
      ),
    );
    renderRoute("/clusters/dev");
    expect(
      await screen.findByText(/No pass rates or recent runs: run history store is not configured/),
    ).toBeInTheDocument();
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
