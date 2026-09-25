import { z } from "zod";
import { StatisticsSchema, WebSocketMessageSchema } from "../types";

type Listener = (event: { data: string }) => void;

// Minimal stand-in for the browser WebSocket that lets tests push messages
// and simulate the server closing the connection.
export class FakeWebSocket {
  static instances: FakeWebSocket[] = [];

  url: string;
  private listeners: Record<string, Set<Listener>> = {};

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  static reset() {
    FakeWebSocket.instances = [];
  }

  static latest(): FakeWebSocket {
    const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    if (!socket) {
      throw new Error("no WebSocket has been opened");
    }
    return socket;
  }

  addEventListener(type: string, listener: Listener) {
    (this.listeners[type] ??= new Set()).add(listener);
  }

  removeEventListener(type: string, listener: Listener) {
    this.listeners[type]?.delete(listener);
  }

  receive(data: string) {
    this.listeners.message?.forEach((listener) => listener({ data }));
  }

  serverClose() {
    this.listeners.close?.forEach((listener) => listener({ data: "" }));
  }
}

function zeroFill(schema: z.ZodTypeAny): unknown {
  if (schema instanceof z.ZodObject) {
    return Object.fromEntries(
      Object.entries(schema.shape as Record<string, z.ZodTypeAny>).map(
        ([key, value]) => [key, zeroFill(value)]
      )
    );
  }
  return 0;
}

export function makeStatsMessage({
  lossRate = 0,
  bitrate = 5,
  rtt = 40,
  type = "reader",
  timestamp = new Date(),
}: {
  lossRate?: number;
  bitrate?: number;
  rtt?: number;
  type?: "reader" | "writer";
  timestamp?: Date;
} = {}): z.infer<typeof WebSocketMessageSchema> {
  const stats = zeroFill(StatisticsSchema) as z.infer<typeof StatisticsSchema>;
  stats.Instantaneous.PktRecvLossRate = lossRate;
  stats.Instantaneous.MbpsRecvRate = bitrate;
  stats.Instantaneous.MsRTT = rtt;
  return { timestamp: timestamp.toISOString(), type, stats };
}
