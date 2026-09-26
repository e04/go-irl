// Replays statistics captured from a real go-irl by the Go E2E suite
// (e2e/*_test.go, E2E_STATS_DIR) through the Browser Source. It lives outside
// src/ because it reads files with Node APIs. Skipped unless E2E_STATS_DIR is
// set; see e2e/run-docker.sh.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { act, render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import App from "../src/App";
import {
  type ConnectionQuality,
  RETRANS_RATE_HISTORY_SIZE,
  nextConnectionQuality,
} from "../src/connectionQuality";
import { retransRate } from "../src/stats";
import { FakeWebSocket } from "../src/test/helpers";
import { WebSocketMessageSchema } from "../src/types";

type Capture = {
  test: string;
  expect: { quality: ConnectionQuality | ConnectionQuality[] };
  messages: unknown[];
};

const dir = process.env.E2E_STATS_DIR;
const captures: Capture[] = dir
  ? readdirSync(dir)
      .filter((name) => name.endsWith(".json"))
      .map((name) => JSON.parse(readFileSync(join(dir, name), "utf8")))
  : [];

// The distinct connection qualities the Browser Source passes through.
function qualityTransitions(rates: number[]): ConnectionQuality[] {
  const history: number[] = [];
  let quality: ConnectionQuality = "unknown";
  const seen: ConnectionQuality[] = [];
  for (const rate of rates) {
    history.push(rate);
    if (history.length > RETRANS_RATE_HISTORY_SIZE) history.shift();
    quality = nextConnectionQuality(quality, history);
    if (quality !== "unknown" && seen[seen.length - 1] !== quality) {
      seen.push(quality);
    }
  }
  return seen;
}

describe.skipIf(!dir)("statistics captured from go-irl", () => {
  it("found captures", () => {
    expect(captures.length).toBeGreaterThan(0);
  });

  describe.each(captures.map((c) => [c.test, c] as const))("%s", (_, capture) => {
    const parsed = capture.messages.map((m) => WebSocketMessageSchema.safeParse(m));
    const readers = parsed.flatMap((p) =>
      p.success && p.data.type === "reader" ? [p.data] : []
    );
    const expected = ([] as ConnectionQuality[]).concat(capture.expect.quality);
    const rates = readers.map((m, i) =>
      retransRate(m.stats, i > 0 ? readers[i - 1].stats : null)
    );

    beforeEach(() => {
      vi.useFakeTimers();
      FakeWebSocket.reset();
      vi.stubGlobal("WebSocket", FakeWebSocket);
      vi.spyOn(console, "log").mockImplementation(() => {});
    });

    it("matches the frontend schema", () => {
      for (const p of parsed) {
        expect(p.error?.errors ?? []).toEqual([]);
      }
      expect(readers.length).toBeGreaterThanOrEqual(3);
    });

    it("drives connection quality as expected", () => {
      expect(
        qualityTransitions(rates.filter((rate) => rate !== null))
      ).toEqual(expected);
    });

    it("switches scenes and shows the latest stats", () => {
      window.history.replaceState(null, "", "/app?type=simple");
      const setCurrentScene = vi.fn();
      vi.stubGlobal("obsstudio", { setCurrentScene });
      const { container } = render(<App />);

      for (const message of capture.messages) {
        act(() => {
          FakeWebSocket.latest().receive(JSON.stringify(message));
          vi.advanceTimersByTime(1000);
        });
      }

      const scenes = expected.map((q) => (q === "good" ? "ONLINE" : "OFFLINE"));
      expect(setCurrentScene.mock.calls.map(([scene]) => scene)).toEqual(scenes);

      const last = readers[readers.length - 1].stats.Instantaneous;
      expect(container.textContent).toContain(`${last.MbpsRecvRate.toFixed(1)}Mbps`);
      expect(container.textContent).toContain(`${last.MsRTT.toFixed(0)}ms`);
      expect(container.textContent).toContain(
        `${rates[rates.length - 1]?.toFixed(1) ?? "-"}%`
      );
    });
  });
});
