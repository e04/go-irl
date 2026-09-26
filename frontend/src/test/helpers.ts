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

type StatsMessageOptions = {
  bitrate?: number;
  rtt?: number;
  type?: "reader" | "writer";
  timestamp?: Date;
};

export function makeStatsMessage({
  bitrate = 5,
  rtt = 40,
  type = "reader",
  timestamp = new Date(),
}: StatsMessageOptions = {}): z.infer<typeof WebSocketMessageSchema> {
  const stats = zeroFill(StatisticsSchema) as z.infer<typeof StatisticsSchema>;
  stats.Instantaneous.MbpsRecvRate = bitrate;
  stats.Instantaneous.MsRTT = rtt;
  return { timestamp: timestamp.toISOString(), type, stats };
}

const PACKETS_PER_SAMPLE = 1000;

// Messages from one SRT connection. The retransmission rate is derived from
// the accumulated counters of consecutive messages, so the first message of a
// stream (and the first after a disconnection) only sets the baseline.
export class StatsStream {
  private msTimeStamp = 0;
  private pktRecv = 0;
  private pktRecvRetrans = 0;
  private linkRxBytes = new Map<number, number>();

  // linkBytes maps SRTLA link IDs to the bytes they received since the
  // previous message; links left out are no longer registered.
  next({
    retransRate = 0,
    linkBytes,
    ...options
  }: StatsMessageOptions & {
    retransRate?: number;
    linkBytes?: Record<number, number>;
  } = {}) {
    this.msTimeStamp += 1000;
    this.pktRecv += PACKETS_PER_SAMPLE;
    this.pktRecvRetrans += Math.round((PACKETS_PER_SAMPLE * retransRate) / 100);

    const message = makeStatsMessage(options);
    message.stats.MsTimeStamp = this.msTimeStamp;
    message.stats.Accumulated.PktRecv = this.pktRecv;
    message.stats.Accumulated.PktRecvRetrans = this.pktRecvRetrans;
    if (linkBytes != null) {
      message.links = Object.entries(linkBytes).map(([key, bytes]) => {
        const id = Number(key);
        const rxBytes = (this.linkRxBytes.get(id) ?? 0) + bytes;
        this.linkRxBytes.set(id, rxBytes);
        return { id, rxBytes };
      });
    }
    return message;
  }
}
