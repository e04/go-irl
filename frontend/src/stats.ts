import { z } from "zod";
import { StatisticsSchema, WebSocketMessageSchema } from "./types";

export type Statistics = z.infer<typeof StatisticsSchema>;
export type StatsMessage = z.infer<typeof WebSocketMessageSchema>;

export type LinkShare = {
  id: number;
  share: number; // 0-1 of the bytes received over all links
};

export type StatsSample = {
  // Client clock at reception. The message timestamp comes from whichever
  // host produced the stats (the remote relay in relay mode), so it can be
  // skewed against the clock the graph's time axis is drawn with.
  receivedAtMs: number;
  bitrate: number; // Mbps
  rtt: number; // ms
  // Share of the packets received since the previous sample that were
  // retransmissions, in percent. This is not unrecovered loss: gosrt does not
  // count packets skipped at the TSBPD deadline, and PktRecvDrop only counts
  // late or duplicate arrivals. null when there is no comparable previous
  // sample or nothing arrived in between.
  retransRate: number | null;
  // Each SRTLA link's share of the bytes received since the previous sample,
  // ordered by link ID. Empty when unknown.
  links: LinkShare[];
};

// gosrt's Instantaneous.PktRecvLossRate is the same ratio, but over its own
// ~1s window that is not aligned with the samples, and it keeps its last value
// while nothing arrives. Deriving it from the accumulated counters avoids both.
export function retransRate(
  current: Statistics,
  previous: Statistics | null
): number | null {
  if (previous == null || current.MsTimeStamp <= previous.MsTimeStamp) {
    return null;
  }
  const recv = current.Accumulated.PktRecv - previous.Accumulated.PktRecv;
  const retrans =
    current.Accumulated.PktRecvRetrans - previous.Accumulated.PktRecvRetrans;
  // Counters going backwards means a new SRT connection.
  if (recv <= 0 || retrans < 0) {
    return null;
  }
  return (retrans / recv) * 100;
}

export function linkShares(
  current: StatsMessage,
  previous: StatsMessage | null
): LinkShare[] {
  if (previous == null || current.links == null) {
    return [];
  }
  const before = new Map(previous.links?.map((l) => [l.id, l.rxBytes]));
  const deltas = current.links.map((link) => {
    // A link missing from the previous message registered since then.
    const delta = link.rxBytes - (before.get(link.id) ?? 0);
    return { id: link.id, delta: Math.max(delta, 0) };
  });
  const total = deltas.reduce((sum, { delta }) => sum + delta, 0);
  if (total === 0) {
    return [];
  }
  return deltas
    .map(({ id, delta }) => ({ id, share: delta / total }))
    .sort((a, b) => a.id - b.id);
}

export function toSample(
  message: StatsMessage,
  previous: StatsMessage | null,
  receivedAtMs: number
): StatsSample {
  return {
    receivedAtMs,
    bitrate: message.stats.Instantaneous.MbpsRecvRate,
    rtt: message.stats.Instantaneous.MsRTT,
    retransRate: retransRate(message.stats, previous?.stats ?? null),
    links: linkShares(message, previous),
  };
}
