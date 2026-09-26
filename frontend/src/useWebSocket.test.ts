import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FakeWebSocket, makeStatsMessage, StatsStream } from "./test/helpers";
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

let stream: StatsStream;

function sendStats(retransRate: number, count = 1) {
  for (let i = 0; i < count; i++) {
    act(() => {
      FakeWebSocket.latest().receive(
        JSON.stringify(stream.next({ retransRate }))
      );
      vi.advanceTimersByTime(1000);
    });
  }
}

// The first message after (re)connecting has no rate of its own.
function sendBaseline() {
  sendStats(0);
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
    stream = new StatsStream();
  });

  it("connects to the given URL", () => {
    setup();
    expect(FakeWebSocket.latest().url).toBe(URL);
  });

  it("reports a good connection after enough low-loss samples", () => {
    const { onGoodConnection, onPoorConnection } = setup();
    sendBaseline();

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
    sendBaseline();
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
    sendBaseline();
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
    sendBaseline();
    sendStats(0, 3);
    sendStats(50, 3);
    expect(onPoorConnection).toHaveBeenCalledTimes(1);

    advance(6000);
    expect(onDisconnected).toHaveBeenCalledTimes(1);

    sendBaseline();
    sendStats(50, 3);
    expect(onGoodConnection).toHaveBeenCalledTimes(1);
    expect(onPoorConnection).toHaveBeenCalledTimes(2);

    sendStats(0, 3);
    expect(onGoodConnection).toHaveBeenCalledTimes(2);
  });

  it("reports a good connection again after reconnecting", () => {
    const { onGoodConnection, onDisconnected } = setup();
    sendBaseline();
    sendStats(0, 3);
    expect(onGoodConnection).toHaveBeenCalledTimes(1);

    advance(6000);
    expect(onDisconnected).toHaveBeenCalledTimes(1);

    sendBaseline();

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

    expect(hook.result.current.samples).toHaveLength(0);
    expect(onConnected).not.toHaveBeenCalled();
    expect(errorSpy).toHaveBeenCalledTimes(2);
  });

  it("keeps only the most recent messages", () => {
    const { hook } = setup();

    act(() => {
      for (let i = 0; i < 150; i++) {
        FakeWebSocket.latest().receive(
          JSON.stringify(makeStatsMessage({ bitrate: i }))
        );
      }
    });

    const { samples } = hook.result.current;
    expect(samples).toHaveLength(120);
    expect(samples[0].bitrate).toBe(30);
    expect(samples[119].bitrate).toBe(149);
  });

  it("derives the retransmission rate from consecutive messages", () => {
    const { hook } = setup();

    sendStats(40);
    expect(hook.result.current.samples[0].retransRate).toBeNull();

    sendStats(12);
    expect(hook.result.current.samples[1].retransRate).toBe(12);

    // A new SRT connection restarts the counters.
    stream = new StatsStream();
    sendStats(30);
    expect(hook.result.current.samples[2].retransRate).toBeNull();
    sendStats(30);
    expect(hook.result.current.samples[3].retransRate).toBe(30);
  });

  it("does not let gosrt's own loss rate drive the scene", () => {
    const { onPoorConnection } = setup();
    sendBaseline();

    for (let i = 0; i < 5; i++) {
      const message = stream.next({ retransRate: 0 });
      message.stats.Instantaneous.PktRecvLossRate = 50;
      act(() => {
        FakeWebSocket.latest().receive(JSON.stringify(message));
      });
    }
    expect(onPoorConnection).not.toHaveBeenCalled();
  });

  it("timestamps samples with the client clock", () => {
    const { hook } = setup();

    act(() => {
      FakeWebSocket.latest().receive(
        JSON.stringify(
          makeStatsMessage({ timestamp: new Date(Date.now() - 3600_000) })
        )
      );
    });

    expect(hook.result.current.samples[0].receivedAtMs).toBe(Date.now());
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
