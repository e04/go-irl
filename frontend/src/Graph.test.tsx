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

function item(timepointUnixMs: number, loss = 0) {
  return { timepointUnixMs, bitrate: 5, rtt: 10, loss };
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
      <Graph data={[item(1000)]} isDisconnected={false} />
    );
    rerender(<Graph data={[item(1000), item(2000)]} isDisconnected={false} />);
    rerender(
      <Graph
        data={[item(1000), item(2000), item(3000, 0.1)]}
        isDisconnected={false}
      />
    );

    expect(echarts.init).toHaveBeenCalledTimes(1);
    expect(chart.dispose).not.toHaveBeenCalled();

    const lastOption = chart.setOption.mock.lastCall?.[0];
    const series = Object.fromEntries(
      lastOption.series.map((s: { id: string; data: unknown }) => [s.id, s.data])
    );
    expect(series.bitrate).toEqual([
      [1000, 5],
      [2000, 5],
      [3000, 5],
    ]);
    // RTT is clamped to the bottom of the log axis.
    expect(series.rtt[0]).toEqual([1000, 20]);
    expect(series.loss[2]).toEqual([3000, 0.1]);
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
      <Graph data={[item(1000), item(2000, 0.25)]} isDisconnected={false} />
    );
    expect(container.textContent).toContain("5.0Mbps");
    expect(container.textContent).toContain("25.0%");
  });
});
