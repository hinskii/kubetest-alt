import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import { setTheme } from "@/lib/theme";
import { renderRoute } from "@/test/render";
import { admin, finishedRun, server, viewer } from "@/test/server";

const R = "*/api/v1/clusters/dev/runs/team-a/smoke-abcde";
const page = "/clusters/dev/runs/team-a/smoke-abcde";
const region = (name: string | RegExp) => within(screen.getByRole("region", { name }));

describe("run page", () => {
  afterEach(() => {
    setTheme("light");
    localStorage.clear();
  });

  it("shows the facts, steps, failed cases, gallery and artifacts of a finished run", async () => {
    renderRoute(page);
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent("smoke-abcde");
    expect(screen.getByText("1 of 2 workers failed")).toBeInTheDocument();
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

    expect(screen.getByRole("link", { name: "Open report" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde/artifacts/report/index.html",
    );
    expect(screen.getByRole("link", { name: "Compare with another run" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde/compare",
    );
    const report = screen.getByTitle("Report of smoke-abcde");
    expect(report).toHaveAttribute("sandbox", "allow-scripts");
    expect(region("Report").getByRole("link", { name: "Download" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde/artifacts/report/index.html?download=1",
    );

    const steps = region("Steps")
      .getAllByRole("row")
      .map((r) => r.textContent);
    expect(steps[1]).toContain("worker-2");
    expect(region(/Failed tests/).getByRole("link", { name: "checkout" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/team-a/smoke/cases/history?case=shop%20%E2%80%BA%20checkout",
    );
    expect(region(/Screenshots/).getByRole("img")).toHaveAttribute(
      "src",
      "/clusters/dev/runs/team-a/smoke-abcde/artifacts/shots/pay%20(failed).png",
    );
    expect(region("Artifacts").getAllByRole("link", { name: "download" })).toHaveLength(2);
    expect(await screen.findByText(/hello/)).toBeInTheDocument();
    expect(region("Log").getByRole("link", { name: "Download" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-abcde/logs.txt",
    );
    expect(screen.queryByRole("button", { name: "Abort run" })).not.toBeInTheDocument();
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
    renderRoute(page);
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
    expect(screen.getByTitle("Live view of smoke-abcde")).toHaveAttribute("src", "/live/token-1/ui/");
    expect(screen.queryByTitle("Report of smoke-abcde")).not.toBeInTheDocument();
    expect(screen.getByText("A comment can be added once the run has finished.")).toBeInTheDocument();
    expect(await screen.findByText(/first/)).toBeInTheDocument();

    await act(() => vi.advanceTimersByTimeAsync(6_000));
    expect(await screen.findByText(/first\s+second/)).toBeInTheDocument();
    expect(offsets).toContain("6");
    expect(screen.getByTitle("Live view of smoke-abcde")).toHaveAttribute("src", "/live/token-1/ui/");
    vi.useRealTimers();

    const abort = within(screen.getByRole("form", { name: "Abort" }));
    await userEvent.type(abort.getByLabelText("Reason"), "wrong env");
    await userEvent.click(abort.getByRole("button", { name: "Abort run" }));
    expect(await screen.findByText("Abort requested.")).toBeInTheDocument();
    expect(aborted).toEqual({ message: "wrong env" });
  });

  it("inverts tool frames in the dark theme unless original colours are chosen", async () => {
    setTheme("dark");
    renderRoute(page);
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

  it("renders the API's 404", async () => {
    server.use(
      http.get(R, () => HttpResponse.json({ error: 'run "smoke-abcde" not found' }, { status: 404 })),
    );
    renderRoute(page);
    expect(await screen.findByRole("alert")).toHaveTextContent('run "smoke-abcde" not found');
  });
});
