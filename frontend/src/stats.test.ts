import { describe, expect, it } from "vitest";
import { linkShares, retransRate, toSample } from "./stats";
import { makeStatsMessage, StatsStream } from "./test/helpers";

describe("retransRate", () => {
  it("is the share of retransmitted packets between two messages", () => {
    const stream = new StatsStream();
    const first = stream.next({ retransRate: 50 });
    const second = stream.next({ retransRate: 12 });

    expect(retransRate(first.stats, null)).toBeNull();
    expect(retransRate(second.stats, first.stats)).toBe(12);
  });

  it("is unknown across a new connection or when nothing arrived", () => {
    const old = new StatsStream();
    old.next();
    const before = old.next();
    const restarted = new StatsStream().next();
    expect(retransRate(restarted.stats, before.stats)).toBeNull();

    const idle = structuredClone(before);
    idle.stats.MsTimeStamp += 1000;
    expect(retransRate(idle.stats, before.stats)).toBeNull();
  });
});

describe("linkShares", () => {
  it("splits the bytes received since the previous message by link", () => {
    const stream = new StatsStream();
    const first = stream.next({ linkBytes: { 1: 100_000, 2: 100_000 } });
    const second = stream.next({ linkBytes: { 2: 25_000, 1: 75_000 } });

    expect(linkShares(first, null)).toEqual([]);
    expect(linkShares(second, first)).toEqual([
      { id: 1, share: 0.75 },
      { id: 2, share: 0.25 },
    ]);
  });

  it("counts links that joined since the previous message in full", () => {
    const stream = new StatsStream();
    const first = stream.next({ linkBytes: { 1: 100_000 } });
    const second = stream.next({ linkBytes: { 1: 30_000, 2: 10_000 } });

    expect(linkShares(second, first)).toEqual([
      { id: 1, share: 0.75 },
      { id: 2, share: 0.25 },
    ]);
  });

  it("is empty without link counters or when no link received anything", () => {
    const stream = new StatsStream();
    const first = stream.next({ linkBytes: { 1: 100 } });
    const idle = stream.next({ linkBytes: { 1: 0 } });

    expect(linkShares(idle, first)).toEqual([]);
    expect(linkShares(makeStatsMessage(), first)).toEqual([]);
  });
});

describe("toSample", () => {
  it("combines the instantaneous values with the derived rates", () => {
    const stream = new StatsStream();
    const first = stream.next({ linkBytes: { 3: 1 } });
    const second = stream.next({
      bitrate: 4,
      rtt: 55,
      retransRate: 10,
      linkBytes: { 3: 1 },
    });

    expect(toSample(second, first, 1234)).toEqual({
      receivedAtMs: 1234,
      bitrate: 4,
      rtt: 55,
      retransRate: 10,
      links: [{ id: 3, share: 1 }],
    });
  });
});
