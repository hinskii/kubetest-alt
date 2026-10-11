import { act, render, screen, within } from "@testing-library/react";

import type { RunDetail } from "@/api/client";
import { RunFlow, stageMs } from "@/components/run/run-flow";

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
    expect(tiles[0]).toHaveTextContent("Queued2.0s");
    expect(within(tiles[2]!).getByText("thresholds crossed")).toBeInTheDocument();
    expect(screen.getAllByTestId("stage-connector")).toHaveLength(3);
    expect(screen.getByRole("list", { name: "Run stages" })).toBeInTheDocument();
  });

  it("counts a running stage's time up, live", () => {
    vi.useFakeTimers({ toFake: ["setInterval", "Date"] });
    vi.setSystemTime(new Date(t(10)));
    render(<RunFlow stages={[{ kind: "run", name: "Run k6", phase: "running", startedAt: t(5) }]} />);
    expect(screen.getByTestId("stage")).toHaveTextContent("Run k65.0s");
    act(() => vi.advanceTimersByTime(3000));
    expect(screen.getByTestId("stage")).toHaveTextContent("Run k68.0s");
  });

  it("draws nothing without stages (composite runs, runs from before)", () => {
    const { container } = render(<RunFlow stages={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("measures a stage only once it has started", () => {
    expect(stageMs({ kind: "queue", name: "Queued", phase: "queued" }, 0)).toBeUndefined();
    expect(stageMs({ kind: "run", name: "Run", phase: "passed", startedAt: t(1), finishedAt: t(4) }, 0)).toBe(
      3000,
    );
  });
});
