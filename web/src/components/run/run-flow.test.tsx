import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";

import type { RunDetail } from "@/api/client";
import { RunFlow, stageMs, stageTime, WorkersFlow } from "@/components/run/run-flow";
import { logOf } from "@/components/run/run-log";
import { renderRoute } from "@/test/render";
import { finishedRun, server } from "@/test/server";

type Stage = RunDetail["stages"][number];

const t = (s: number) => new Date(Date.UTC(2026, 9, 11, 12, 0, s)).toISOString();

describe("RunFlow", () => {
  afterEach(() => vi.useRealTimers());

  it("draws the stages in order with their state and time", () => {
    const stages: Stage[] = [
      { kind: "queue", name: "Queued", phase: "passed", startedAt: t(0), finishedAt: t(2) },
      { kind: "fetch", name: "Clone git repository", phase: "passed", startedAt: t(2), finishedAt: t(3) },
      {
        kind: "run",
        name: "Run k6",
        phase: "failed",
        startedAt: t(3),
        finishedAt: t(13),
        message: "thresholds crossed",
      },
      { kind: "collect", name: "Collect results", phase: "skipped" },
    ];
    render(<RunFlow stages={stages} />);
    const tiles = screen.getAllByTestId("stage");
    expect(tiles.map((s) => s.getAttribute("data-phase"))).toEqual(["passed", "passed", "failed", "skipped"]);
    expect(tiles[0]).toHaveTextContent("Queued2.00s");
    expect(within(tiles[2]!).getByText("thresholds crossed")).toBeInTheDocument();
    expect(screen.getAllByTestId("stage-connector")).toHaveLength(3);
    expect(screen.getByRole("list", { name: "Run stages" })).toBeInTheDocument();
  });

  it("counts a running stage's time up, live", () => {
    vi.useFakeTimers({ toFake: ["setInterval", "Date"] });
    vi.setSystemTime(new Date(t(10)));
    render(<RunFlow stages={[{ kind: "run", name: "Run k6", phase: "running", startedAt: t(5) }]} />);
    expect(screen.getByTestId("stage")).toHaveTextContent("Run k65.00s");
    act(() => vi.advanceTimersByTime(250));
    expect(screen.getByTestId("stage")).toHaveTextContent("Run k65.25s");
    act(() => vi.advanceTimersByTime(3000));
    expect(screen.getByTestId("stage")).toHaveTextContent("Run k68.25s");
  });

  it("draws nothing without stages (composite runs, runs from before)", () => {
    const { container } = render(<RunFlow stages={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("measures a stage only once it has started", () => {
    expect(stageMs({ phase: "queued" }, 0)).toBeUndefined();
    expect(stageMs({ phase: "passed", startedAt: t(1), finishedAt: t(4) }, 0)).toBe(3000);
  });
});

describe("stageTime", () => {
  it("reads like a stopwatch", () => {
    expect(stageTime(420)).toBe("0.42s");
    expect(stageTime(34_270)).toBe("34.27s");
    expect(stageTime(65_300)).toBe("1m 05.3s");
    expect(stageTime(3_720_000)).toBe("1h 02m");
  });
});

describe("WorkersFlow", () => {
  it("fans a parallel run's workers out, each picking its log", async () => {
    const picked: string[] = [];
    render(
      <WorkersFlow
        steps={[
          { key: "worker-0", kind: "Worker", phase: "passed", startedAt: t(0), finishedAt: t(4) },
          { key: "worker-1", kind: "Worker", phase: "failed", message: "exit code 1" },
          { key: "attempt-1", kind: "Try", phase: "failed" },
        ]}
        selected="worker-1"
        onSelect={(k) => picked.push(k)}
      />,
    );
    expect(screen.getByRole("region", { name: "Workers" })).toHaveTextContent("Workers · 1 of 2 passed");
    expect(screen.getAllByTestId("stage")).toHaveLength(2);
    expect(screen.getByText("exit code 1")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Show the log of worker 1" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await userEvent.click(screen.getByRole("button", { name: "Show the log of worker 0" }));
    expect(picked).toEqual(["worker-0"]);
  });

  it("draws each worker's own stages, with its matrix values", async () => {
    const picked: string[] = [];
    render(
      <WorkersFlow
        steps={[
          {
            key: "worker-0",
            kind: "Worker",
            phase: "running",
            startedAt: t(0),
            params: { shard: "1", browser: "chrome" },
            stages: [
              { kind: "queue", name: "Queued", phase: "passed", startedAt: t(0), finishedAt: t(1) },
              { kind: "run", name: "Run k6", phase: "running", startedAt: t(1) },
            ],
          },
        ]}
        selected="worker-0/run"
        onSelect={(k) => picked.push(k)}
      />,
    );
    expect(screen.getByTestId("worker")).toHaveTextContent("Worker 0browser=chrome, shard=1· running");
    const row = within(screen.getByRole("list", { name: "Worker 0 stages" }));
    expect(row.getAllByTestId("stage").map((s) => s.getAttribute("data-phase"))).toEqual([
      "passed",
      "running",
    ]);
    expect(row.getByRole("button", { name: "Show the log of worker 0: Run k6" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await userEvent.click(row.getByRole("button", { name: "Show the log of worker 0: Queued" }));
    expect(picked).toEqual(["worker-0/queue"]);
  });

  it("draws nothing for a run without workers", () => {
    const { container } = render(<WorkersFlow steps={[{ key: "s0", kind: "Step", phase: "passed" }]} />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("ChildrenFlow", () => {
  it("draws a composite run's child runs in order, each a link to it", async () => {
    server.use(
      http.get("*/api/v1/clusters/dev/runs/team-a/suite-1", () =>
        HttpResponse.json({
          ...finishedRun,
          id: "suite-1",
          name: "suite-1",
          stages: [],
          steps: [],
          children: [
            {
              id: "suite-1-login",
              name: "suite-1-login",
              testRef: "login",
              phase: "passed",
              durationMs: 2100,
            },
            { id: "suite-1-pay", name: "suite-1-pay", testRef: "pay", phase: "running", startedAt: t(0) },
          ],
        }),
      ),
    );
    renderRoute("/clusters/dev/runs/team-a/suite-1");
    const flow = within(await screen.findByRole("list", { name: "Child runs" }));
    expect(flow.getAllByTestId("stage").map((s) => s.getAttribute("data-phase"))).toEqual([
      "passed",
      "running",
    ]);
    expect(flow.getByRole("link", { name: "Open suite-1-login (login, passed)" })).toHaveAttribute(
      "href",
      "/clusters/dev/runs/team-a/suite-1-login",
    );
    expect(flow.getByText("2.10s")).toBeInTheDocument();
  });
});

describe("logOf", () => {
  it("finds a stage's log, a worker's too", () => {
    expect(logOf()).toEqual({ stage: undefined, part: undefined });
    expect(logOf("fetch")).toEqual({ stage: "fetch" });
    expect(logOf("run")).toEqual({ stage: undefined, part: "tool" });
    expect(logOf("queue")).toEqual({ queue: true });
    expect(logOf("worker-2")).toEqual({ stage: "worker-2", part: undefined });
    expect(logOf("worker-2/fetch")).toEqual({ stage: "worker-2/fetch" });
    expect(logOf("worker-2/collect")).toEqual({ stage: "worker-2", part: "collect" });
    expect(logOf("worker-2/queue")).toEqual({ queue: true });
  });
});
