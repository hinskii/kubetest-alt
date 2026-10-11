import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import { setTheme } from "@/lib/theme";
import { renderRoute } from "@/test/render";
import { admin, finishedRun, server, viewer } from "@/test/server";

const R = "*/api/v1/clusters/dev/runs/team-a/smoke-abcde";
const page = "/clusters/dev/runs/team-a/smoke-abcde";
const region = (name: string | RegExp) => within(screen.getByRole("region", { name }));
const openTab = (name: RegExp) => userEvent.click(screen.getByRole("tab", { name }));

describe("run page", () => {
  afterEach(() => {
    setTheme("light");
    localStorage.clear();
  });

  it("shows a finished run: the log first, the rest in tabs with counts", async () => {
    const { router } = renderRoute(page);
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent("smoke-abcde");
    expect(screen.getByText("1 of 2 workers failed")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open report" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde/artifacts/report/index.html",
    );
    expect(screen.getByRole("link", { name: "Compare with another run" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde/compare",
    );
    expect(screen.queryByRole("button", { name: "Abort run" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual([
      "Log",
      "Report",
      "Tests2",
      "Artifacts2",
      "Steps2",
      "Details",
      "Events",
    ]);
    expect(screen.getByRole("tab", { name: "Log" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByText(/hello/)).toBeInTheDocument();
    expect(region(/^Log/).getByRole("link", { name: "Download" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde/logs.txt",
    );

    await openTab(/^Report/);
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: "report" }));
    expect(screen.getByTitle("Report of smoke-abcde")).toHaveAttribute("sandbox", "allow-scripts");
    expect(region("Report").getByRole("link", { name: "Download" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde/artifacts/report/index.html?download=1",
    );

    await openTab(/^Tests/);
    expect(region(/Failed tests/).getByText(/1 of 5 · 3 passed, 0 skipped/)).toBeInTheDocument();
    expect(region(/Failed tests/).getByRole("link", { name: "checkout" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/team-a/smoke/cases/history?case=shop+%E2%80%BA+checkout",
    );

    await openTab(/^Artifacts/);
    expect(region(/Screenshots/).getByRole("img")).toHaveAttribute(
      "src",
      "/clusters/dev/runs/team-a/smoke-abcde/artifacts/shots/pay%20(failed).png",
    );
    expect(region("Artifacts").getAllByRole("link", { name: "download" })).toHaveLength(2);

    await openTab(/^Steps/);
    expect(region("Steps").getAllByRole("row")[1]!.textContent).toContain("worker-2");

    await openTab(/^Details/);
    const facts = within(screen.getByRole("region", { name: "Facts" }));
    expect(facts.getByRole("link", { name: "smoke" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/team-a/smoke",
    );
    expect(facts.getByText("ui · dev@example.com")).toBeInTheDocument();
    expect(facts.getByRole("link", { name: "0123456" })).toHaveAttribute(
      "href",
      "https://github.com/acme/tests/commit/0123456789abcdef",
    );
    expect(facts.getByText("3 passed · 2 failed · 0 skipped · 5 total")).toBeInTheDocument();
    expect(facts.getByText("120.568")).toBeInTheDocument();
  });

  it("opens the tab the address names", async () => {
    renderRoute(`${page}?tab=artifacts`);
    expect(await screen.findByRole("tab", { name: /^Artifacts/ })).toHaveAttribute("aria-selected", "true");
    expect(region("Artifacts").getAllByRole("link", { name: "download" })).toHaveLength(2);
  });

  it("lets developers comment a finished run and remove it again", async () => {
    const sent: { method: string; body?: unknown }[] = [];
    server.use(
      http.put(`${R}/comment`, async ({ request }) => {
        sent.push({ method: "PUT", body: await request.json() });
        return HttpResponse.json({ text: "known flake", by: "dev@example.com", at: "2026-10-07T13:00:00Z" });
      }),
      http.delete(`${R}/comment`, () => {
        sent.push({ method: "DELETE" });
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderRoute(`${page}?tab=details`);
    const form = within(await screen.findByRole("form", { name: "Comment" }));
    await userEvent.type(form.getByLabelText("Comment text"), "known flake");
    await userEvent.click(form.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Comment saved.")).toBeInTheDocument();
    await userEvent.clear(form.getByLabelText("Comment text"));
    await userEvent.click(form.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Comment removed.")).toBeInTheDocument();
    expect(sent).toEqual([{ method: "PUT", body: { text: "known flake" } }, { method: "DELETE" }]);
  });

  it("runs again with the same parameters and opens the new run", async () => {
    server.use(
      http.post(`${R}/rerun`, () =>
        HttpResponse.json({ name: "smoke-new", namespace: "team-a" }, { status: 201 }),
      ),
    );
    const { router } = renderRoute(page);
    await userEvent.click(await screen.findByRole("button", { name: "Run again" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/clusters/dev/runs/team-a/smoke-new"));
    expect(
      await screen.findByText("Run started again with the parameters of smoke-abcde."),
    ).toBeInTheDocument();
  });

  it("says why it couldn't run again", async () => {
    server.use(
      http.post(`${R}/rerun`, () =>
        HttpResponse.json({ error: "Test smoke no longer exists." }, { status: 409 }),
      ),
    );
    renderRoute(page);
    await userEvent.click(await screen.findByRole("button", { name: "Run again" }));
    expect(
      await screen.findByText("Could not start the run: Test smoke no longer exists."),
    ).toBeInTheDocument();
  });

  it("lets admins delete a finished run, then shows its Test", async () => {
    let deleted = false;
    server.use(
      http.get("*/api/v1/me", () => HttpResponse.json(admin)),
      http.delete(R, () => {
        deleted = true;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const { router } = renderRoute(page);
    await userEvent.click(await screen.findByRole("button", { name: "Delete run" }));
    await userEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete run smoke-abcde" }),
    );
    await waitFor(() => expect(router.state.location.pathname).toBe("/clusters/dev/tests/team-a/smoke"));
    expect(deleted).toBe(true);
    expect(await screen.findByText("Run smoke-abcde deleted.")).toBeInTheDocument();
  });

  it("gives viewers no actions", async () => {
    server.use(http.get("*/api/v1/me", () => HttpResponse.json(viewer)));
    renderRoute(page);
    await screen.findByRole("heading", { level: 1 });
    expect(screen.queryByRole("button", { name: "Run again" })).not.toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Comment" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete run" })).not.toBeInTheDocument();
  });

  it("follows a live run: activity, live view kept, log polled, abort", async () => {
    const live = {
      ...finishedRun,
      phase: "running",
      finished: false,
      finishedAt: undefined,
      failedCases: [],
      liveView: "/live/token-1/ui/",
      events: [
        {
          time: "2026-10-07T12:00:00Z",
          type: "Normal",
          reason: "Scheduled",
          object: "Pod/p",
          message: "assigned",
        },
        {
          time: "2026-10-07T12:00:10Z",
          type: "Warning",
          reason: "BackOff",
          object: "Pod/p",
          message: "pulling slowly",
        },
      ],
    };
    let signed = 1;
    const offsets: string[] = [];
    let aborted: unknown;
    server.use(
      http.get(R, () => HttpResponse.json({ ...live, liveView: `/live/token-${signed++}/ui/` })),
      http.get(`${R}/log`, ({ request }) => {
        const offset = new URL(request.url).searchParams.get("offset") ?? "0";
        offsets.push(offset);
        return offset === "0"
          ? HttpResponse.text("first\n", { headers: { "X-Run-Phase": "running", "X-Next-Offset": "6" } })
          : HttpResponse.text("second\n", { headers: { "X-Run-Phase": "running", "X-Next-Offset": "13" } });
      }),
      http.post(`${R}/abort`, async ({ request }) => {
        aborted = await request.json();
        return new HttpResponse(null, { status: 204 });
      }),
    );
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderRoute(page);
    expect(await screen.findByRole("status")).toHaveTextContent("Now: BackOff — pulling slowly");
    expect(screen.queryByRole("tab", { name: /^Report/ })).not.toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /^Events/ })).toHaveTextContent("Events2");
    expect(await screen.findByText(/first/)).toBeInTheDocument();
    await act(() => vi.advanceTimersByTimeAsync(6_000));
    expect(await screen.findByText(/first\s+second/)).toBeInTheDocument();
    expect(offsets).toContain("6");

    // The live view keeps its first signed link while the run refreshes.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "Live view" }));
    expect(await screen.findByTitle("Live view of smoke-abcde")).toHaveAttribute("src", "/live/token-1/ui/");
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    expect(screen.getByTitle("Live view of smoke-abcde")).toHaveAttribute("src", "/live/token-1/ui/");
    vi.useRealTimers();

    await openTab(/^Details/);
    expect(screen.getByText("A comment can be added once the run has finished.")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Abort run" }));
    const abort = within(await screen.findByRole("form", { name: "Abort" }));
    await userEvent.type(abort.getByLabelText("Reason"), "wrong env");
    await userEvent.click(abort.getByRole("button", { name: "Abort run" }));
    expect(await screen.findByText("Abort requested.")).toBeInTheDocument();
    expect(aborted).toEqual({ message: "wrong env" });
  });

  it("inverts tool frames in the dark theme unless original colours are chosen", async () => {
    setTheme("dark");
    renderRoute(`${page}?tab=report`);
    const frame = (await screen.findByTitle("Report of smoke-abcde")).parentElement!;
    expect(frame).not.toHaveAttribute("data-original");
    await userEvent.click(screen.getByRole("button", { name: "Original colors" }));
    expect(frame).toHaveAttribute("data-original");
    expect(localStorage.getItem("frame-colors")).toBe("original");
    await userEvent.click(screen.getByRole("button", { name: "Match the dark theme" }));
    expect(frame).not.toHaveAttribute("data-original");
    act(() => setTheme("light"));
    expect(
      screen.queryByRole("button", { name: /Original colors|Match the dark theme/ }),
    ).not.toBeInTheDocument();
  });

  it("waits for output when a live run hasn't logged anything yet", async () => {
    server.use(
      http.get(R, () =>
        HttpResponse.json({ ...finishedRun, phase: "queued", finished: false, failedCases: [] }),
      ),
      http.get(
        `${R}/log`,
        () => new HttpResponse(null, { headers: { "X-Run-Phase": "queued", "X-Next-Offset": "0" } }),
      ),
    );
    renderRoute(page);
    expect(await screen.findByText("Waiting for output…")).toBeInTheDocument();
    expect(screen.queryByText(/Log unavailable/)).not.toBeInTheDocument();
  });

  it("renders the API's 404", async () => {
    server.use(
      http.get(R, () => HttpResponse.json({ error: 'run "smoke-abcde" not found' }, { status: 404 })),
    );
    renderRoute(page);
    expect(await screen.findByRole("alert")).toHaveTextContent('run "smoke-abcde" not found');
  });
});

describe("the run's stages", () => {
  const page = "/clusters/dev/runs/team-a/smoke-abcde";
  const log = `k6 output
done
--- kubetest: collecting results ---
uploaded 3 artifacts
`;

  it("shows the tool's part of the log by default, and each stage's on a click", async () => {
    const asked: string[] = [];
    server.use(
      http.get(`${R}/log`, ({ request }) => {
        const stage = new URL(request.url).searchParams.get("stage") ?? "";
        asked.push(stage);
        return HttpResponse.text(stage === "fetch" ? "Cloning into '/data/repo'...\n" : log, {
          headers: { "X-Run-Phase": "failed", "X-Next-Offset": "99" },
        });
      }),
    );
    const { router } = renderRoute(page);
    const logPanel = await screen.findByRole("region", { name: "Log · Run k6" });
    expect(await within(logPanel).findByText(/k6 output/)).toBeInTheDocument();
    expect(within(logPanel).queryByText(/uploaded 3 artifacts/)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Show the log of Collect results" }));
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: "log", stage: "collect" }));
    const collect = await screen.findByRole("region", { name: "Log · Collect results" });
    expect(await within(collect).findByText(/uploaded 3 artifacts/)).toBeInTheDocument();
    expect(within(collect).queryByText(/k6 output/)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Show the log of Clone git repository" }));
    expect(
      await within(await screen.findByRole("region", { name: "Log · Clone git repository" })).findByText(
        /Cloning into/,
      ),
    ).toBeInTheDocument();
    expect(asked).toContain("fetch");
    expect(screen.getByRole("button", { name: "Show the log of Clone git repository" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("shows what Kubernetes said for the queue", async () => {
    renderRoute(`${page}?stage=queue`);
    const queue = await screen.findByRole("region", { name: "Log · Queued" });
    expect(within(queue).getByTestId("run-log")).toBeInTheDocument();
  });
});
