import { useState, useEffect, useRef } from "react";
import { WebSocketMessageSchema } from "./types";
import { type StatsMessage, type StatsSample, toSample } from "./stats";
import {
  type ConnectionQuality,
  RETRANS_RATE_HISTORY_SIZE,
  nextConnectionQuality,
} from "./connectionQuality";

// Stats arrive about once per second; keep a bit more than the graph window.
const MAX_SAMPLES = 120;
const CONNECTION_WAIT_TIME = 5000;
const RECONNECT_DELAY = 1000;
// Re-render periodically so disconnection is detected even without messages.
const TICK_INTERVAL = 500;

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
  const [samples, setSamples] = useState<StatsSample[]>([]);
  const [, setTick] = useState(0);
  const socket = useRef<WebSocket | null>(null);
  const lastReceivedTime = useRef<number>(0);
  const previousConnectionState = useRef<boolean | null>(null);
  // Rates are derived from the accumulated counters of consecutive messages.
  const previousMessage = useRef<StatsMessage | null>(null);

  const retransRateHistory = useRef<number[]>([]);
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

  const updateConnectionQuality = (retransRate: number) => {
    retransRateHistory.current.push(retransRate);
    if (retransRateHistory.current.length > RETRANS_RATE_HISTORY_SIZE) {
      retransRateHistory.current.shift();
    }

    const next = nextConnectionQuality(
      connectionQualityRef.current,
      retransRateHistory.current
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

    const now = Date.now();
    const sample = toSample(parsed.data, previousMessage.current, now);
    previousMessage.current = parsed.data;
    if (sample.retransRate !== null) {
      updateConnectionQuality(sample.retransRate);
    }

    lastReceivedTime.current = now;

    setSamples((prev) => {
      const next = [...prev, sample];
      if (next.length > MAX_SAMPLES) next.shift();
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
        retransRateHistory.current = [];
        previousMessage.current = null;
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

  return { samples, isDisconnected };
}
