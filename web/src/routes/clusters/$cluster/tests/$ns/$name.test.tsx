import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import { bucketLabel } from "@/components/test/overview-tiles";
import { renderRoute } from "@/test/render";
import { admin, runs, server, smoke, viewer } from "@/test/server";

const T = "*/api/v1/clusters/dev/tests/team-a/smoke";
const page = "/clusters/dev/tests/team-a/smoke";

describe("test page", () => {
  it("shows the header, links and the run history", async () => {
    renderRoute(page);
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent("smoke");
    expect(screen.getByText("Namespace team-a · templates base")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Edit" })).toHaveAttribute("href", `${page}?tab=settings`);
    expect(screen.getByTestId("schedule-line")).toHaveTextContent(
      "Runs on a schedule: 0 2 * * * · next 2026-10-08 02:00:00 UTC · change",
    );
    expect(screen.getByRole("link", { name: "Duplicate" })).toHaveAttribute(
      "href",
      "/clusters/dev/tests/new?namespace=team-a&from=smoke",
    );
    expect(screen.queryByRole("button", { name: "Delete test" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual([
      "Overview",
      "Runs",
      "Insights",
      "CLI commands",
      "Settings",
    ]);
  });

  it("shows the week's runs as tiles, and another range on request", async () => {
    const asked: string[] = [];
    server.use(
      http.get(`${T}/overview`, ({ request }) => {
        const range = new URL(request.url).searchParams.get("range") ?? "";
        asked.push(range);
        return HttpResponse.json({
          range,
          since: "2026-10-01T12:00:00Z",
          summary: {
            finished: 12,
            passed: 9,
            failed: 2,
            errored: 1,
            aborted: 0,
            passRate: "75%",
            avgMs: 5600,
            p95Ms: 9000,
          },
          p50Ms: 5000,
          buckets: [{ start: "2026-10-06T00:00:00Z", total: 12, passed: 9, failed: 3, avgMs: 5600 }],
          capped: range === "30d",
        });
      }),
    );
    const { router } = renderRoute(page);
    const tiles = await screen.findAllByTestId("overview-tile");
    expect(tiles.map((t) => t.textContent)).toEqual([
      "Pass rate75%",
      "Failed runs3",
      "Total runs12",
      "Average duration5.6sP50 5.0s · P95 9.0s",
    ]);
    // Picking a day on any chart shows that day on every tile.
    const failed = screen.getByRole("slider", { name: "Failed runs over time" });
    act(() => failed.focus());
    await waitFor(() =>
      expect(screen.getAllByTestId("tile-value").map((v) => v.textContent)).toEqual([
        "75%",
        "3",
        "12",
        "5.6s",
      ]),
    );
    expect(screen.getAllByTestId("tile-period").map((p) => p.textContent)).toEqual([
      "Oct 6",
      "Oct 6",
      "Oct 6",
      "Oct 6",
    ]);
    expect(failed).toHaveAttribute("aria-valuetext", "Oct 6: 3 failed of 12");
    act(() => failed.blur());
    await waitFor(() => expect(screen.queryAllByTestId("tile-period")).toHaveLength(0));
    await userEvent.click(screen.getByRole("button", { name: "30d" }));
    await waitFor(() => expect(router.state.location.search).toEqual({ range: "30d" }));
    expect(await screen.findByText("the newest 500 counted")).toBeInTheDocument();
    expect(asked).toEqual(["7d", "30d"]);
  });

  it("lists the runs in their own tab, filtered by status and searched", async () => {
    const asked: string[] = [];
    server.use(
      http.get(`${T}/runs`, ({ request }) => {
        asked.push(new URL(request.url).searchParams.get("phase") ?? "");
        return HttpResponse.json({ runs });
      }),
    );
    const { router } = renderRoute(`${page}?tab=runs`);
    const panel = within(await screen.findByRole("region", { name: "Runs" }));
    expect(await panel.findByRole("link", { name: "smoke-git" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/smoke-git",
    );
    expect(panel.getByRole("link", { name: "smoke-old" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/uid-1",
    );
    expect(panel.getByRole("link", { name: "0123456" })).toHaveAttribute(
      "href",
      "https://github.com/acme/tests/commit/0123456789abcdef",
    );
    expect(panel.getByText("ui · dev@example.com")).toBeInTheDocument();
    expect(panel.getByText("5.6s")).toBeInTheDocument();
    expect(panel.getByRole("link", { name: "Clean up runs" })).toBeInTheDocument();

    await userEvent.type(panel.getByLabelText("Search runs"), "baseline");
    await waitFor(() => expect(panel.queryByRole("link", { name: "smoke-old" })).not.toBeInTheDocument());
    expect(panel.getByRole("link", { name: "smoke-git" })).toBeInTheDocument();

    await userEvent.click(panel.getByRole("combobox", { name: "Status" }));
    await userEvent.click(await screen.findByRole("option", { name: "failed" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ tab: "runs", phase: "failed" }));
    await waitFor(() => expect(asked).toContain("failed"));
  });

  it("gives ready CLI commands", async () => {
    renderRoute(`${page}?tab=cli`);
    expect(await screen.findByText("kubectl kubetest run smoke -n team-a --follow")).toBeInTheDocument();
    expect(
      screen.getByText("kubectl kubetest run smoke -n team-a --config env=stage --config vus=10 --follow"),
    ).toBeInTheDocument();
  });

  it("validates the run form and starts a run with the values", async () => {
    let body: unknown;
    server.use(
      http.post(`${T}/runs`, async ({ request }) => {
        body = await request.json();
        return HttpResponse.json({ name: "smoke-x1", namespace: "team-a" }, { status: 201 });
      }),
    );
    const { router } = renderRoute(page);
    const form = within(await screen.findByRole("form", { name: "Run" }));
    expect(form.getByLabelText(/vus/)).toHaveValue("10");

    await userEvent.click(form.getByRole("button", { name: "Run test" }));
    expect(await form.findByText("target is required")).toBeInTheDocument();
    await userEvent.type(form.getByLabelText(/target/), "http://plain");
    await userEvent.click(form.getByRole("button", { name: "Run test" }));
    expect(await form.findByText("must match ^https://")).toBeInTheDocument();

    await userEvent.clear(form.getByLabelText(/target/));
    await userEvent.type(form.getByLabelText(/target/), "https://shop");
    await userEvent.click(form.getByRole("button", { name: "Run test" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/clusters/dev/runs/team-a/smoke-x1"));
    expect(body).toEqual({ config: { env: "stage", target: "https://shop", vus: "10" } });
    expect(await screen.findByText("Run started.")).toBeInTheDocument();
  });

  it("sends a start time as UTC", async () => {
    let body: { notBefore?: string } = {};
    server.use(
      http.post(`${T}/runs`, async ({ request }) => {
        body = (await request.json()) as typeof body;
        return HttpResponse.json(
          { name: "smoke-x2", namespace: "team-a", notBefore: "2030-01-02T03:04:00Z" },
          { status: 201 },
        );
      }),
    );
    const { router } = renderRoute(page);
    const form = within(await screen.findByRole("form", { name: "Run" }));
    await userEvent.type(form.getByLabelText(/target/), "https://shop");
    await userEvent.type(form.getByLabelText(/Start at/), "2030-01-02T03:04");
    await userEvent.click(form.getByRole("button", { name: "Run test" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/clusters/dev/runs/team-a/smoke-x2"));
    expect(body.notBefore).toBe("2030-01-02T03:04:00Z");
    expect(await screen.findByText("Run scheduled for 2030-01-02 03:04 UTC.")).toBeInTheDocument();
  });

  it("shows the API's refusal to start", async () => {
    server.use(http.post(`${T}/runs`, () => HttpResponse.json({ error: "quota exceeded" }, { status: 400 })));
    renderRoute(page);
    const form = within(await screen.findByRole("form", { name: "Run" }));
    await userEvent.type(form.getByLabelText(/target/), "https://shop");
    await userEvent.click(form.getByRole("button", { name: "Run test" }));
    expect(await screen.findByText("Could not start the run: quota exceeded")).toBeInTheDocument();
  });

  it("saves and removes the schedule", async () => {
    const sent: string[] = [];
    server.use(
      http.put(`${T}/schedule`, async ({ request }) => {
        const { schedule } = (await request.json()) as { schedule: string };
        sent.push(schedule);
        return HttpResponse.json(
          schedule ? { schedule: { expr: schedule, next: ["2026-10-08T02:00:00Z"] } } : {},
        );
      }),
    );
    renderRoute(`${page}?tab=settings`);
    const panel = within(await screen.findByRole("region", { name: "Schedule" }));
    expect(panel.getByText("0 2 * * *")).toBeInTheDocument();
    const input = panel.getByLabelText(/Cron/);
    await userEvent.clear(input);
    await userEvent.type(input, "0 3 * * *");
    await userEvent.click(panel.getByRole("button", { name: "Save schedule" }));
    expect(await screen.findByText("Schedule saved. Next run: 2026-10-08 02:00:00 UTC.")).toBeInTheDocument();
    await userEvent.click(panel.getByRole("button", { name: "Remove schedule" }));
    expect(await screen.findByText("Schedule removed.")).toBeInTheDocument();
    expect(sent).toEqual(["0 3 * * *", ""]);
  });

  it("is read-only for a Git-managed Test, with the reason it can't run", async () => {
    server.use(
      http.get(T, () =>
        HttpResponse.json({ ...smoke, locked: true, schedule: undefined, problem: "set spec.config.script" }),
      ),
      http.get("*/api/v1/me", () => HttpResponse.json(admin)),
    );
    renderRoute(page);
    expect(await screen.findByText("read-only")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(
      "Not ready to run: set spec.config.script — set it where the Test is defined (Git).",
    );
    expect(screen.queryByRole("link", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Duplicate" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete test" })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Schedule" })).not.toBeInTheDocument();
  });

  it("gives viewers no run form, no schedule form, no edit", async () => {
    server.use(http.get("*/api/v1/me", () => HttpResponse.json(viewer)));
    renderRoute(`${page}?tab=runs`);
    await screen.findByRole("region", { name: "Runs" });
    expect(screen.queryByRole("form", { name: "Run" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save schedule" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Clean up runs" })).not.toBeInTheDocument();
  });

  it("lets admins delete after confirming", async () => {
    let deleted = false;
    server.use(
      http.get("*/api/v1/me", () => HttpResponse.json(admin)),
      http.delete(T, () => {
        deleted = true;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const { router } = renderRoute(page);
    await userEvent.click(await screen.findByRole("button", { name: "Delete test" }));
    const dialog = within(await screen.findByRole("alertdialog"));
    expect(dialog.getByText(/its run history, logs and artifacts stay/)).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("button", { name: "Delete test smoke" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/clusters/dev"));
    expect(deleted).toBe(true);
    expect(await screen.findByText("Test team-a/smoke deleted. Its run history stays.")).toBeInTheDocument();
  });

  it("pages through older runs", async () => {
    const asked: (string | null)[] = [];
    server.use(
      http.get(`${T}/runs`, ({ request }) => {
        const after = new URL(request.url).searchParams.get("after");
        asked.push(after);
        return HttpResponse.json(
          after
            ? { runs: [{ id: "old-1", name: "old-1", phase: "passed" }] }
            : { runs: [{ id: "new-1", name: "new-1", phase: "passed" }], next: "c1" },
        );
      }),
    );
    renderRoute(`${page}?tab=runs`);
    await userEvent.click(await screen.findByRole("link", { name: "Older runs" }));
    expect(await screen.findByRole("link", { name: "old-1" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Newest" })).toBeInTheDocument();
    expect(asked).toContain("c1");
  });

  it("shows No runs yet, and the API's error for a missing Test", async () => {
    server.use(http.get(`${T}/runs`, () => HttpResponse.json({ runs: [] })));
    renderRoute(page);
    expect(await screen.findByText("No runs yet.")).toBeInTheDocument();
  });

  it("renders the API's 404", async () => {
    server.use(http.get(T, () => HttpResponse.json({ error: 'tests "smoke" not found' }, { status: 404 })));
    renderRoute(page);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Something went wrong (404)");
    expect(alert).toHaveTextContent('tests "smoke" not found');
  });

  it("toasts a notice from a server-rendered page and drops it from the address", async () => {
    const { router } = renderRoute(`${page}?notice=Test%20saved.`);
    expect(await screen.findByText("Test saved.")).toBeInTheDocument();
    await waitFor(() => expect(router.state.location.searchStr).toBe(""));
  });
});

describe("bucketLabel", () => {
  it("names hours for a day, days otherwise, in UTC", () => {
    expect(bucketLabel("2026-10-07T14:00:00Z", "24h")).toBe("Oct 7 14:00 UTC");
    expect(bucketLabel("2026-10-07T00:00:00Z", "7d")).toBe("Oct 7");
  });
});
