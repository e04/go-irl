import { act, render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";
import { FakeWebSocket, makeStatsMessage } from "./test/helpers";

vi.mock("./Graph", () => ({
  Graph: () => <div data-testid="graph" />,
}));

function renderApp(query: string) {
  window.history.replaceState(null, "", `/app${query}`);
  const setCurrentScene = vi.fn();
  vi.stubGlobal("obsstudio", { setCurrentScene });
  const view = render(<App />);
  return { setCurrentScene, ...view };
}

function sendStats(lossRate: number, count = 1) {
  for (let i = 0; i < count; i++) {
    act(() => {
      FakeWebSocket.latest().receive(
        JSON.stringify(makeStatsMessage({ lossRate, bitrate: 5, rtt: 40 }))
      );
      vi.advanceTimersByTime(1000);
    });
  }
}

describe("App", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeWebSocket.reset();
    vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.spyOn(console, "log").mockImplementation(() => {});
  });

  it("connects to the WebSocket port from the query string", () => {
    renderApp("?wsport=1234");
    expect(FakeWebSocket.latest().url).toBe("ws://localhost:1234/ws");
  });

  it("defaults to port 8888", () => {
    renderApp("");
    expect(FakeWebSocket.latest().url).toBe("ws://localhost:8888/ws");
  });

  it("switches between the configured scenes", () => {
    const { setCurrentScene } = renderApp(
      "?onlineSceneName=Live&offlineSceneName=BRB"
    );

    sendStats(0, 3);
    expect(setCurrentScene).toHaveBeenLastCalledWith("Live");

    sendStats(50, 3);
    expect(setCurrentScene).toHaveBeenLastCalledWith("BRB");

    act(() => {
      vi.advanceTimersByTime(6000);
    });
    expect(setCurrentScene).toHaveBeenLastCalledWith("BRB");

    // Reconnecting with a still-lossy link must not go back online.
    sendStats(50, 3);
    expect(setCurrentScene).not.toHaveBeenLastCalledWith("Live");

    sendStats(0, 3);
    expect(setCurrentScene).toHaveBeenLastCalledWith("Live");
  });

  it("shows the latest stats in the simple view", () => {
    const { container } = renderApp("?type=simple");

    sendStats(12);

    expect(container.textContent).toContain("5.0Mbps");
    expect(container.textContent).toContain("40ms");
    expect(container.textContent).toContain("12.0%");
  });

  it("renders the graph view when requested", () => {
    const { getByTestId } = renderApp("?type=graph");
    expect(getByTestId("graph")).toBeTruthy();
  });

  it("renders nothing for type=none", () => {
    const { container } = renderApp("?type=none");
    sendStats(0);
    expect(container.innerHTML).toBe("");
  });
});
