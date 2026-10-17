import type { Event, EventPattern } from "./event.js";

/**
 * RequestType classifies a delivery request sent from a worker to the bus.
 *
 * These are the API-names in the `type` field of the HTTP publish payload —
 * never change the serialized strings without changing the Go transport.
 */
export type RequestType = "send" | "broadcast";

/** Message type sent from a worker to the bus (worker → bus direction). */
export type MessageType = RequestType | "heartbeat" | "disconnect";

/**
 * A delivery request from a worker to the bus. Mirrors `core/bus.Request`.
 * Serialized over HTTP as the publish body; `MessageType` is the wire `type`.
 */
export interface WorkerMessage {
  type: MessageType;
  events: Event[];
  targets?: string[];
  trace_id?: string;
}

/** Publish detail: which event types a worker may publish. */
export interface Identity {
  worker_id: string;
  credential: string;
  publish_allow?: string[];
  subscribe_allow?: EventPattern[];
}
