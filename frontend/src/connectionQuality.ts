export const RETRANS_RATE_HISTORY_SIZE = 3;
export const HIGH_RETRANS_RATE_THRESHOLD = 20;
export const LOW_RETRANS_RATE_THRESHOLD = 5;

export type ConnectionQuality = "unknown" | "good" | "poor";

export function nextConnectionQuality(
  current: ConnectionQuality,
  history: number[]
): ConnectionQuality {
  if (history.length < RETRANS_RATE_HISTORY_SIZE) {
    return current;
  }
  const allHighLoss = history.every(
    (rate) => rate >= HIGH_RETRANS_RATE_THRESHOLD
  );
  const allLowLoss = history.every((rate) => rate < LOW_RETRANS_RATE_THRESHOLD);

  if (current === "poor") {
    return allLowLoss ? "good" : "poor";
  }
  // Both "unknown" and "good" only drop to "poor" on sustained high loss.
  return allHighLoss ? "poor" : "good";
}
