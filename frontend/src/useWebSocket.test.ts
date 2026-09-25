import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FakeWebSocket, makeStatsMessage } from "./test/helpers";
import { useWebSocket } from "./useWebSocket";

const URL = "ws://localhost:8888/ws";

function setup() {
  const callbacks = {
    onConnected: vi.fn(),
    onDisconnected: vi.fn(),
    onPoorConnection: vi.fn(),
    onGoodConnection: vi.fn(),
  };
  const hook = renderHook(() => useWebSocket({ url: URL, ...callbacks }));
  return { ...callbacks, hook };
}

function sendStats(lossRate: number, count = 1) {
  for (let i = 0; i < count; i++) {
    act(() => {
      FakeWebSocket.latest().receive(
        JSON.stringify(makeStatsMessage({ lossRate }))
      );
      vi.advanceTimersByTime(1000);
    });
  }
}

function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

describe("useWebSocket", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeWebSocket.reset();
    vi.stubGlobal("WebSocket", FakeWebSocket);
  });

  it("connects to the given URL", () => {
    setup();
    expect(FakeWebSocket.latest().url).toBe(URL);
  });

  it("reports a good connection after enough low-loss samples", () => {
    const { onGoodConnection, onPoorConnection } = setup();

    sendStats(0, 2);
    expect(onGoodConnection).not.toHaveBeenCalled();

    sendStats(0);
    expect(onGoodConnection).toHaveBeenCalledTimes(1);
    expect(onPoorConnection).not.toHaveBeenCalled();

    sendStats(0, 5);
    expect(onGoodConnection).toHaveBeenCalledTimes(1);
  });

  it("switches between poor and good with hysteresis", () => {
    const { onGoodConnection, onPoorConnection } = setup();
    sendStats(0, 3);

    sendStats(50, 3);
    expect(onPoorConnection).toHaveBeenCalledTimes(1);

    // Moderate loss is not enough to recover.
    sendStats(10, 3);
    expect(onGoodConnection).toHaveBeenCalledTimes(1);

    sendStats(1, 3);
    expect(onGoodConnection).toHaveBeenCalledTimes(2);
  });

  it("does not flag a poor connection on intermittent loss spikes", () => {
    const { onPoorConnection } = setup();
    sendStats(0, 3);

    sendStats(50, 2);
    sendStats(0);
    sendStats(50, 2);
    expect(onPoorConnection).not.toHaveBeenCalled();
  });

  it("detects disconnection and reconnection from message flow", () => {
    const { onConnected, onDisconnected, hook } = setup();
    expect(hook.result.current.isDisconnected).toBe(true);

    sendStats(0);
    expect(onConnected).toHaveBeenCalledTimes(1);
    expect(hook.result.current.isDisconnected).toBe(false);

    advance(3000);
    expect(onDisconnected).not.toHaveBeenCalled();

    advance(2500);
    expect(onDisconnected).toHaveBeenCalledTimes(1);
    expect(hook.result.current.isDisconnected).toBe(true);

    sendStats(0);
    expect(onConnected).toHaveBeenCalledTimes(2);
  });

  it("stays offline when reconnecting while the link is still lossy", () => {
    const { onGoodConnection, onPoorConnection, onDisconnected } = setup();
    sendStats(0, 3);
    sendStats(50, 3);
    expect(onPoorConnection).toHaveBeenCalledTimes(1);

    advance(6000);
    expect(onDisconnected).toHaveBeenCalledTimes(1);

    sendStats(50, 3);
    expect(onGoodConnection).toHaveBeenCalledTimes(1);
    expect(onPoorConnection).toHaveBeenCalledTimes(2);

    sendStats(0, 3);
    expect(onGoodConnection).toHaveBeenCalledTimes(2);
  });

  it("reports a good connection again after reconnecting", () => {
    const { onGoodConnection, onDisconnected } = setup();
    sendStats(0, 3);
    expect(onGoodConnection).toHaveBeenCalledTimes(1);

    advance(6000);
    expect(onDisconnected).toHaveBeenCalledTimes(1);

    // The offline scene set on disconnect is only lifted via a fresh
    // good-connection report, even if the link was good before.
    sendStats(10, 2);
    expect(onGoodConnection).toHaveBeenCalledTimes(1);
    sendStats(10);
    expect(onGoodConnection).toHaveBeenCalledTimes(2);
  });

  it("ignores malformed, invalid and writer messages", () => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const { onConnected, hook } = setup();

    act(() => {
      const socket = FakeWebSocket.latest();
      socket.receive("not json");
      socket.receive(JSON.stringify({ type: "reader" }));
      socket.receive(JSON.stringify(makeStatsMessage({ type: "writer" })));
    });

    expect(hook.result.current.messages).toHaveLength(0);
    expect(onConnected).not.toHaveBeenCalled();
    expect(errorSpy).toHaveBeenCalledTimes(2);
  });

  it("keeps only the most recent messages", () => {
    const { hook } = setup();

    act(() => {
      for (let i = 0; i < 150; i++) {
        FakeWebSocket.latest().receive(
          JSON.stringify(makeStatsMessage({ lossRate: i }))
        );
      }
    });

    const { messages } = hook.result.current;
    expect(messages).toHaveLength(120);
    expect(messages[0].stats.Instantaneous.PktRecvLossRate).toBe(30);
    expect(messages[119].stats.Instantaneous.PktRecvLossRate).toBe(149);
  });

  it("reopens the WebSocket after it closes", () => {
    setup();
    expect(FakeWebSocket.instances).toHaveLength(1);

    act(() => {
      FakeWebSocket.latest().serverClose();
    });
    advance(999);
    expect(FakeWebSocket.instances).toHaveLength(1);

    advance(1);
    expect(FakeWebSocket.instances).toHaveLength(2);
  });
});
