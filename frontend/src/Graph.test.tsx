import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Graph } from "./Graph";

const chart = {
  setOption: vi.fn(),
  resize: vi.fn(),
  dispose: vi.fn(),
};

vi.mock("echarts", () => ({
  init: vi.fn(() => chart),
}));

const echarts = await import("echarts");

function item(
  receivedAtMs: number,
  retransRate: number | null = 0,
  links: { id: number; share: number }[] = []
) {
  return { receivedAtMs, bitrate: 5, rtt: 10, retransRate, links };
}

function lastAxis() {
  const updates = chart.setOption.mock.calls
    .map(([option]) => option.xAxis)
    .filter((axis) => axis?.max != null);
  return updates[updates.length - 1];
}

function lastSeries() {
  const [option, opts] = chart.setOption.mock.lastCall ?? [];
  expect(opts).toEqual({ replaceMerge: ["series"] });
  return Object.fromEntries(
    option.series.map((s: { id: string; data: unknown }) => [s.id, s.data])
  );
}

describe("Graph", () => {
  beforeEach(() => {
    vi.mocked(echarts.init).mockClear();
    chart.setOption.mockClear();
    chart.resize.mockClear();
    chart.dispose.mockClear();
  });

  it("creates the chart once and only updates it afterwards", () => {
    const { rerender } = render(
      <Graph data={[item(1500)]} isDisconnected={false} />
    );
    rerender(<Graph data={[item(1500), item(2500)]} isDisconnected={false} />);
    rerender(
      <Graph
        data={[item(1500, null), item(2500), item(3500, 10)]}
        isDisconnected={false}
      />
    );

    expect(echarts.init).toHaveBeenCalledTimes(1);
    expect(chart.dispose).not.toHaveBeenCalled();

    // Samples are drawn in the middle of the second before they arrived. Bars
    // are [Mbps, time] so that ECharts stacks the bitrate.
    const series = lastSeries();
    expect(series.bitrate).toEqual([
      [5, 1000],
      [5, 2000],
      [5, 3000],
    ]);
    // RTT is clamped to the bottom of the log axis.
    expect(series.rtt[0]).toEqual([1000, 20]);
    // Missing and zero rates are hidden.
    expect(series.retrans).toEqual([
      [1000, -Infinity],
      [2000, -Infinity],
      [3000, 10],
    ]);
  });

  it("stacks each link's share of the bitrate", () => {
    render(
      <Graph
        data={[
          item(1500),
          item(2500, 0, [
            { id: 1, share: 0.6 },
            { id: 2, share: 0.4 },
          ]),
          item(3500, 0, [{ id: 2, share: 1 }]),
        ]}
        isDisconnected={false}
      />
    );

    const series = lastSeries();
    // The first sample has no shares yet and is drawn unattributed.
    expect(series.bitrate).toEqual([
      [5, 1000],
      [0, 2000],
      [0, 3000],
    ]);
    expect(series["link-1"]).toEqual([
      [0, 1000],
      [3, 2000],
      [0, 3000],
    ]);
    expect(series["link-2"]).toEqual([
      [0, 1000],
      [2, 2000],
      [5, 3000],
    ]);
  });

  it("drops the series of links that left the window", () => {
    const { rerender } = render(
      <Graph data={[item(1500, 0, [{ id: 1, share: 1 }])]} isDisconnected={false} />
    );
    rerender(
      <Graph data={[item(2500, 0, [{ id: 2, share: 1 }])]} isDisconnected={false} />
    );

    expect(Object.keys(lastSeries())).toEqual(["link-2", "rtt", "retrans"]);
  });

  it("ends the time axis at the latest sample while stats arrive", () => {
    vi.useFakeTimers();
    try {
      const receivedAt = Date.now();
      const { unmount } = render(
        <Graph data={[item(receivedAt)]} isDisconnected={false} />
      );
      expect(lastAxis()).toEqual({
        min: receivedAt - 60_000,
        max: receivedAt,
      });

      // Until the next sample is due the newest bar stays at the right edge.
      vi.advanceTimersByTime(1000);
      expect(lastAxis().max).toBe(receivedAt);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("scrolls the time axis with the clock once stats stop", () => {
    vi.useFakeTimers();
    try {
      const receivedAt = Date.now();
      const { unmount } = render(
        <Graph data={[item(receivedAt)]} isDisconnected={false} />
      );
      vi.advanceTimersByTime(10_000);

      expect(lastAxis()).toEqual({
        min: Date.now() - 1500 - 60_000,
        max: Date.now() - 1500,
      });
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("follows the clock before any stats arrive", () => {
    render(<Graph data={[]} isDisconnected={true} />);
    expect(lastAxis().max).toBeCloseTo(Date.now(), -2);
  });

  it("resizes the chart with the window and disposes it on unmount", () => {
    const { unmount } = render(<Graph data={[]} isDisconnected={false} />);

    window.dispatchEvent(new Event("resize"));
    expect(chart.resize).toHaveBeenCalledTimes(1);

    unmount();
    expect(chart.dispose).toHaveBeenCalledTimes(1);
    window.dispatchEvent(new Event("resize"));
    expect(chart.resize).toHaveBeenCalledTimes(1);
  });

  it("shows the latest values while connected", () => {
    const { container } = render(
      <Graph data={[item(1500), item(2500, 25)]} isDisconnected={false} />
    );
    expect(container.textContent).toContain("5.0Mbps");
    expect(container.textContent).toContain("25.0%");
  });
});
