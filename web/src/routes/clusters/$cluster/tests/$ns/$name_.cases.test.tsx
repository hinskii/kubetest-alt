import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import { renderRoute } from "@/test/render";
import { server } from "@/test/server";

const base = { suite: "", class: "shop", lastAt: "2026-10-07T12:00:00Z", skipped: 0 };
const list = {
  window: 30,
  windows: [10, 30, 100],
  flaky: 1,
  failingNow: 2,
  cases: [
    {
      ...base,
      key: "shop › refund",
      name: "refund",
      runs: 10,
      passed: 0,
      failed: 10,
      avgMs: 50,
      maxMs: 60,
      lastStatus: "failed",
      flips: 0,
      flaky: false,
    },
    {
      ...base,
      key: "shop › checkout",
      name: "checkout",
      runs: 10,
      passed: 6,
      failed: 4,
      avgMs: 600,
      maxMs: 900,
      lastStatus: "failed",
      flips: 7,
      flaky: true,
    },
    {
      ...base,
      key: "shop › login",
      name: "login",
      runs: 10,
      passed: 10,
      failed: 0,
      avgMs: 110,
      maxMs: 120,
      lastStatus: "passed",
      flips: 0,
      flaky: false,
    },
  ],
};
const C = "*/api/v1/clusters/dev/tests/team-a/smoke/cases";
const page = "/clusters/dev/tests/team-a/smoke/cases";
const names = () =>
  screen.getAllByTestId("case-row").map((r) => within(r).getAllByRole("link")[0]!.textContent);

describe("test cases", () => {
  it("lists cases most failures first, with the summary and pass rate", async () => {
    let asked = "";
    server.use(
      http.get(C, ({ request }) => {
        asked = new URL(request.url).search;
        return HttpResponse.json(list);
      }),
    );
    renderRoute(page);
    expect(await screen.findByTestId("cases-summary")).toHaveTextContent(
      "3 cases over the last 30 runs that reported JUnit · 2 failing in the latest run · 1 flaky",
    );
    expect(asked).toBe("?runs=30");
    expect(names()).toEqual(["refund", "checkout", "login"]);
    const checkout = within(screen.getAllByTestId("case-row")[1]!);
    expect(checkout.getByText("flaky")).toBeInTheDocument();
    expect(checkout.getByText("60%")).toBeInTheDocument();
    expect(checkout.getByRole("link", { name: "checkout" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/team-a/smoke/cases/history?case=shop+%E2%80%BA+checkout",
    );
  });

  it("sorts from the address and from a column header", async () => {
    server.use(http.get(C, () => HttpResponse.json(list)));
    const { router } = renderRoute(`${page}?sort=flaky`);
    await screen.findAllByTestId("case-row");
    expect(names()).toEqual(["checkout", "refund", "login"]);
    await userEvent.click(screen.getByRole("button", { name: /Avg/ }));
    await waitFor(() => expect(router.state.location.search).toEqual({ sort: "slowest" }));
    expect(names()).toEqual(["checkout", "login", "refund"]);
    await userEvent.click(screen.getByRole("button", { name: /Test case/ }));
    await waitFor(() => expect(names()).toEqual(["checkout", "login", "refund"].sort()));
  });

  it("asks for another window", async () => {
    const asked: string[] = [];
    server.use(
      http.get(C, ({ request }) => {
        asked.push(new URL(request.url).searchParams.get("runs") ?? "");
        return HttpResponse.json(list);
      }),
    );
    renderRoute(`${page}?runs=10`);
    await screen.findAllByTestId("case-row");
    expect(asked).toEqual(["10"]);
  });

  it("explains an empty list", async () => {
    server.use(http.get(C, () => HttpResponse.json({ ...list, cases: [], flaky: 0, failingNow: 0 })));
    renderRoute(page);
    expect(await screen.findByText(/No test cases yet/)).toBeInTheDocument();
  });
});

describe("a case's history", () => {
  it("shows each run's result, newest first", async () => {
    server.use(
      http.get(`${C}/history`, ({ request }) => {
        expect(new URL(request.url).searchParams.get("case")).toBe("shop › checkout");
        return HttpResponse.json({
          key: "shop › checkout",
          passed: 1,
          failed: 1,
          skipped: 0,
          runs: [
            {
              runId: "uid-1",
              runName: "smoke-abcde",
              finishedAt: "2026-10-07T12:00:00Z",
              status: "failed",
              message: "timeout after 5s",
            },
            { runId: "uid-0", runName: "smoke-older", finishedAt: "2026-10-07T11:00:00Z", status: "passed" },
          ],
        });
      }),
    );
    renderRoute(`${page}/history?case=${encodeURIComponent("shop › checkout")}`);
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent("shop › checkout");
    expect(await screen.findByTestId("history-summary")).toHaveTextContent(
      "2 runs · 1 passed · 1 failed · 0 skipped",
    );
    expect(screen.getByRole("link", { name: "smoke-abcde: failed" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/uid-1",
    );
    expect(screen.getByText("timeout after 5s")).toBeInTheDocument();
  });
});

describe("compare a run", () => {
  const R = "*/api/v1/clusters/dev/runs/team-a/smoke-abcde/compare";
  const run = { id: "smoke-abcde", name: "smoke-abcde", phase: "failed" };
  const older = {
    id: "smoke-older",
    name: "smoke-older",
    phase: "passed",
    finishedAt: "2026-10-07T11:00:00Z",
  };
  const empty = {
    startedFailing: [],
    stillFailing: [],
    fixed: [],
    added: [],
    removed: [],
    unchanged: 0,
    runCases: 0,
    baseCases: 0,
  };

  it("offers the runs to compare with, then shows what changed", async () => {
    server.use(
      http.get(R, ({ request }) =>
        HttpResponse.json(
          new URL(request.url).searchParams.get("with")
            ? {
                ...empty,
                run,
                testRef: "smoke",
                candidates: [older],
                base: older,
                runCases: 5,
                baseCases: 5,
                startedFailing: [
                  {
                    key: "shop › checkout",
                    before: "passed",
                    now: "failed",
                    durationDiffMs: 600,
                    message: "timeout",
                  },
                ],
                removed: [{ key: "shop › legacy", before: "passed" }],
                unchanged: 1,
              }
            : { ...empty, run, testRef: "smoke", candidates: [older] },
        ),
      ),
    );
    const { router } = renderRoute("/clusters/dev/runs/team-a/smoke-abcde/compare");
    await userEvent.click(await screen.findByRole("combobox", { name: "with an earlier run" }));
    await userEvent.click(await screen.findByRole("option", { name: /smoke-older/ }));
    await waitFor(() => expect(router.state.location.search).toEqual({ with: "smoke-older" }));
    expect(await screen.findByTestId("compare-summary")).toHaveTextContent(
      "smoke-abcde (failed, 5 cases) against smoke-older (passed, 5 cases)",
    );
    const started = within(screen.getByRole("region", { name: /Started failing/ }));
    expect(started.getByText("shop › checkout")).toBeInTheDocument();
    expect(started.getByText("+600ms")).toBeInTheDocument();
    expect(
      within(screen.getByRole("region", { name: /No longer run/ })).getByText("shop › legacy"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: /Fixed/ })).not.toBeInTheDocument();
    expect(screen.getByText("Unchanged: 1")).toBeInTheDocument();
  });

  it("is linked from a finished run", async () => {
    renderRoute("/clusters/dev/runs/team-a/smoke-abcde");
    expect(await screen.findByRole("link", { name: "Compare with another run" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde/compare",
    );
  });
});
