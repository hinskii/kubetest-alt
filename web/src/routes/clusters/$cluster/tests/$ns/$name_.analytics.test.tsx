import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import { renderRoute } from "@/test/render";
import { server } from "@/test/server";

const A = "*/api/v1/clusters/dev/tests/team-a/smoke/analytics";
const page = "/clusters/dev/tests/team-a/smoke/analytics";
const runs = [
  { id: "uid-3", name: "smoke-3", phase: "error", startedAt: "2026-10-07T12:20:00Z" },
  {
    id: "smoke-2",
    name: "smoke-2",
    phase: "failed",
    startedAt: "2026-10-07T12:10:00Z",
    durationMs: 30000,
    comment: "flake",
  },
  { id: "smoke-1", name: "smoke-1", phase: "passed", startedAt: "2026-10-07T12:00:00Z", durationMs: 30000 },
];
const analytics = {
  window: 30,
  windows: [10, 30, 100],
  runs,
  summary: {
    finished: 3,
    passed: 1,
    failed: 1,
    errored: 1,
    aborted: 0,
    passRate: "33%",
    avgMs: 30000,
    p95Ms: 30000,
    lastFailure: runs[0],
  },
  trends: [
    {
      label: "Duration",
      unit: "ms",
      values: [30000, 30000],
      latest: 30000,
      min: 30000,
      avg: 30000,
      max: 30000,
    },
    {
      label: "p95_ms",
      unit: "",
      values: [100, 150],
      latest: 150,
      min: 100,
      avg: 125,
      max: 150,
      delta: "+50%",
    },
  ],
  cases: { flaky: 1, failingNow: 2 },
};
const comparison = {
  runs: [runs[2], runs[1]],
  facts: [{ label: "Status", cells: [{ value: "passed" }, { value: "failed" }] }],
  metrics: [{ label: "p95_ms", cells: [{ value: "100" }, { value: "150", delta: "+50%" }] }],
  params: [{ label: "vus", cells: [{ value: "10" }, { value: "20" }] }],
  missing: ["other-1"],
  markdown: "## Test comparison: smoke\n| p95_ms | 100 | 150 (+50%) |\n",
};

describe("analytics", () => {
  beforeEach(() => server.use(http.get(A, () => HttpResponse.json(analytics))));

  it("summarizes the window: strip, stats, trends", async () => {
    renderRoute(page);
    const overview = within(await screen.findByRole("region", { name: /Overview/ }));
    expect(overview.getByText("3 finished runs")).toBeInTheDocument();
    expect(overview.getByText("33%")).toBeInTheDocument();
    expect(overview.getByText("1 · 1 · 1 · 0")).toBeInTheDocument();
    expect(overview.getByText("30.0s · 30.0s")).toBeInTheDocument();
    const strip = overview
      .getAllByRole("link", { name: /^smoke-\d: / })
      .map((a) => a.getAttribute("aria-label"));
    expect(strip).toEqual(["smoke-1: passed", "smoke-2: failed", "smoke-3: error"]);
    expect(overview.getByRole("link", { name: "1 flaky · 2 failing now" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/team-a/smoke/cases?runs=30&sort=flaky",
    );

    const p95 = within(screen.getByTestId("trend-p95_ms"));
    expect(p95.getByRole("img", { name: "p95_ms trend" })).toBeInTheDocument();
    expect(p95.getByText("+50%")).toBeInTheDocument();
    expect(within(screen.getByTestId("trend-Duration")).getAllByText("30.0s").length).toBeGreaterThan(0);
    expect(screen.queryByRole("region", { name: "Comparison" })).not.toBeInTheDocument();
  });

  it("compares the picked runs, oldest first, with Markdown to download", async () => {
    let asked: string[] = [];
    server.use(
      http.get(`${A}/compare`, ({ request }) => {
        asked = new URL(request.url).searchParams.getAll("run");
        return HttpResponse.json(comparison);
      }),
    );
    const createObjectURL = vi.fn(() => "blob:md");
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL, revokeObjectURL }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    const { router } = renderRoute(page);
    const pick = within(await screen.findByRole("region", { name: "Compare runs" }));
    await userEvent.click(pick.getByRole("checkbox", { name: "compare smoke-2" }));
    await userEvent.click(pick.getByRole("checkbox", { name: "compare smoke-1" }));
    await userEvent.click(pick.getByRole("button", { name: "Compare selected" }));
    await waitFor(() => expect(router.state.location.search).toEqual({ run: ["smoke-2", "smoke-1"] }));

    const cmp = within(await screen.findByRole("region", { name: "Comparison" }));
    expect(asked).toEqual(["smoke-2", "smoke-1"]);
    const headers = cmp.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers.slice(1)).toEqual(["smoke-1", "smoke-2"]);
    expect(cmp.getByText("Not runs of this Test (skipped): other-1")).toBeInTheDocument();
    expect(cmp.getByText("(+50%)")).toBeInTheDocument();
    expect(cmp.getByRole("textbox", { name: "Comparison as Markdown" })).toHaveValue(comparison.markdown);

    await userEvent.click(cmp.getByRole("button", { name: "Download Markdown" }));
    expect(createObjectURL).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
    click.mockRestore();
    vi.unstubAllGlobals();
  });

  it("says when nothing has finished yet", async () => {
    server.use(
      http.get(A, () => HttpResponse.json({ ...analytics, runs: [], trends: [], cases: undefined })),
    );
    renderRoute(page);
    expect(await screen.findByText(/No finished runs yet/)).toBeInTheDocument();
  });

  it("is linked from the Test page", async () => {
    renderRoute("/clusters/dev/tests/team-a/smoke");
    expect(await screen.findByRole("link", { name: "Analytics & comparison" })).toHaveAttribute("href", page);
  });
});
