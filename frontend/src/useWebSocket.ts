import { useState, useEffect, useRef } from "react";
import { WebSocketMessageSchema } from "./types";
import { z } from "zod";

// Stats arrive about once per second; keep a bit more than the graph window.
const MAX_MESSAGES = 120;
const CONNECTION_WAIT_TIME = 5000;
const LOSS_RATE_HISTORY_SIZE = 3;
const HIGH_LOSS_RATE_THRESHOLD = 20;
const LOW_LOSS_RATE_THRESHOLD = 5;
const RECONNECT_DELAY = 1000;
// Re-render periodically so disconnection is detected even without messages.
const TICK_INTERVAL = 500;

type ConnectionQuality = "unknown" | "good" | "poor";

function nextConnectionQuality(
  current: ConnectionQuality,
  history: number[]
): ConnectionQuality {
  if (history.length < LOSS_RATE_HISTORY_SIZE) {
    return current;
  }
  const allHighLoss = history.every(
    (rate) => rate >= HIGH_LOSS_RATE_THRESHOLD
  );
  const allLowLoss = history.every((rate) => rate < LOW_LOSS_RATE_THRESHOLD);

  if (current === "poor") {
    return allLowLoss ? "good" : "poor";
  }
  // Both "unknown" and "good" only drop to "poor" on sustained high loss.
  return allHighLoss ? "poor" : "good";
}

export function useWebSocket({
  url,
  onConnected,
  onDisconnected,
  onPoorConnection,
  onGoodConnection,
}: {
  url: string;
  onConnected?: () => void;
  onDisconnected?: () => void;
  onPoorConnection?: () => void;
  onGoodConnection?: () => void;
}) {
  const [messages, setMessages] = useState<
    z.infer<typeof WebSocketMessageSchema>[]
  >([]);
  const [, setTick] = useState(0);
  const socket = useRef<WebSocket | null>(null);
  const lastReceivedTime = useRef<number>(0);
  const previousConnectionState = useRef<boolean | null>(null);

  const lossRateHistory = useRef<number[]>([]);
  // "unknown" until enough samples arrive after (re)connecting. Scene
  // switching to online only happens via a transition to "good", so a
  // reconnect with a still-lossy link does not flip back to the online scene.
  const connectionQualityRef = useRef<ConnectionQuality>("unknown");

  const connect = () => {
    if (socket.current) {
      return;
    }
    socket.current = new WebSocket(url);
    socket.current.addEventListener("message", handleMessage);
    socket.current.addEventListener("close", handleClose);
  };

  const updateConnectionQuality = (lossRate: number) => {
    lossRateHistory.current.push(lossRate);
    if (lossRateHistory.current.length > LOSS_RATE_HISTORY_SIZE) {
      lossRateHistory.current.shift();
    }

    const next = nextConnectionQuality(
      connectionQualityRef.current,
      lossRateHistory.current
    );
    if (next === connectionQualityRef.current) {
      return;
    }
    connectionQualityRef.current = next;
    if (next === "poor") {
      onPoorConnection?.();
    } else if (next === "good") {
      onGoodConnection?.();
    }
  };

  const handleMessage = (event: MessageEvent) => {
    let data: unknown;
    try {
      data = JSON.parse(event.data);
    } catch (e) {
      console.error(e);
      return;
    }
    const parsed = WebSocketMessageSchema.safeParse(data);
    if (!parsed.success) {
      console.error(parsed.error.errors);
      return;
    }
    if (parsed.data.type !== "reader") {
      // Ignore non-reader messages
      return;
    }

    const lossRate = parsed.data.stats?.Instantaneous?.PktRecvLossRate;
    if (typeof lossRate === "number") {
      updateConnectionQuality(lossRate);
    }

    lastReceivedTime.current = Date.now();

    setMessages((prev) => {
      const next = [...prev, parsed.data];
      if (next.length > MAX_MESSAGES) next.shift();
      return next;
    });
  };

  const handleClose = () => {
    socket.current?.removeEventListener("message", handleMessage);
    socket.current?.removeEventListener("close", handleClose);
    socket.current = null;
    setTimeout(() => connect(), RECONNECT_DELAY);
  };

  useEffect(() => {
    const intervalId = setInterval(() => {
      setTick((tick) => tick + 1);
    }, TICK_INTERVAL);

    return () => clearInterval(intervalId);
  }, []);

  useEffect(() => {
    connect();

    return () => {
      handleClose();
    };
  }, []);

  const isDisconnected =
    Date.now() - lastReceivedTime.current > CONNECTION_WAIT_TIME;

  useEffect(() => {
    const currentDisconnectedState = isDisconnected;

    if (previousConnectionState.current !== null) {
      if (
        previousConnectionState.current === false &&
        currentDisconnectedState === true
      ) {
        // Stale samples must not decide the scene after reconnecting.
        lossRateHistory.current = [];
        connectionQualityRef.current = "unknown";
        onDisconnected?.();
      } else if (
        previousConnectionState.current === true &&
        currentDisconnectedState === false
      ) {
        onConnected?.();
      }
    }

    previousConnectionState.current = currentDisconnectedState;
  }, [
    isDisconnected,
    onConnected,
    onDisconnected,
    onPoorConnection,
    onGoodConnection,
  ]);

  return { messages, isDisconnected };
}
