import { useState, useEffect, useRef } from "react";
import { WebSocketMessageSchema } from "./types";
import { z } from "zod";

const MAX_MESSAGES = 3000;
const CONNECTION_WAIT_TIME = 5000;
const LOSS_RATE_HISTORY_SIZE = 3;
const HIGH_LOSS_RATE_THRESHOLD = 20;
const LOW_LOSS_RATE_THRESHOLD = 5;
const RECEIVING_INDICATOR_DURATION = 100;
const RECONNECT_DELAY = 1000;
const MESSAGE_INTERVAL = 32;

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
    (z.infer<typeof WebSocketMessageSchema> | null)[]
  >(Array.from({ length: MAX_MESSAGES }, () => null));
  const socket = useRef<WebSocket | null>(null);
  const lastReceivedTime = useRef<number>(0);
  const [isReceiving, setIsReceiving] = useState(false);
  const previousConnectionState = useRef<boolean | null>(null);

  const lossRateHistory = useRef<number[]>([]);
  const connectionQualityRef = useRef<"good" | "poor">("good");

  const connect = () => {
    if (socket.current) {
      return;
    }
    socket.current = new WebSocket(url);
    socket.current.addEventListener("message", handleMessage);
    socket.current.addEventListener("close", handleClose);
  };

  const handleMessage = (event: MessageEvent) => {
    setMessages((prev) => {
      const data = JSON.parse(event.data);
      const parsed = WebSocketMessageSchema.safeParse(data);
      if (!parsed.success) {
        console.error(parsed.error.errors);
        return prev;
      }
      if (parsed.data.type !== "reader") {
        // Ignore non-reader messages
        return prev;
      }

      const lossRate = parsed.data.stats?.Instantaneous?.PktRecvLossRate;
      if (typeof lossRate === "number") {
        lossRateHistory.current.push(lossRate);
        if (lossRateHistory.current.length > LOSS_RATE_HISTORY_SIZE) {
          lossRateHistory.current.shift();
        }

        if (lossRateHistory.current.length === LOSS_RATE_HISTORY_SIZE) {
          const allHighLoss = lossRateHistory.current.every(
            (rate) => rate >= HIGH_LOSS_RATE_THRESHOLD
          );
          const allLowLoss = lossRateHistory.current.every(
            (rate) => rate < LOW_LOSS_RATE_THRESHOLD
          );

          if (connectionQualityRef.current === "good" && allHighLoss) {
            connectionQualityRef.current = "poor";
            onPoorConnection?.();
          } else if (connectionQualityRef.current === "poor" && allLowLoss) {
            connectionQualityRef.current = "good";
            onGoodConnection?.();
          }
        }
      }

      const now = Date.now();
      lastReceivedTime.current = now;

      setIsReceiving(true);
      setTimeout(() => setIsReceiving(false), RECEIVING_INDICATOR_DURATION);

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
      setMessages((prev) => {
        const next = [...prev, null];
        if (next.length > MAX_MESSAGES) next.shift();
        return next;
      });
    }, MESSAGE_INTERVAL);

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

  return { messages, isReceiving, isDisconnected };
}
