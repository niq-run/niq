import type { Event } from "./event.js";

/**
 * Extension describes an event a worker responds to. It is how a worker is
 * extended: each registration teaches the base to recognize one more event
 * and answer it with a handler. The event type IS the extension's identity —
 * distinct things are distinct events. Mirrors `baseworker.Extension`.
 */
export interface Extension {
  /** The event type the extension responds to — its identity. */
  event: string;
  /**
   * Marks an extension only the declaring worker itself serves (e.g.
   * send_message, list_workers). Left out of the peer-facing `worker.ready`
   * announcement, so peers never see it as a callable tool.
   */
  selfOnly?: boolean;
  description?: string;
  parameters?: Record<string, unknown>;
}

/**
 * Handler executed when the registered event arrives on the bus. A pure
 * closure — it captures whatever state it needs, so the signature carries no
 * base-type dependency. Mirrors `baseworker.ExtensionHandler`.
 */
export type ExtensionHandler = (evt: Event) => void | Promise<void>;

/** Parsed, common fields of a tool-invocation event. Mirrors `baseworker.ToolCall`. */
export interface ToolCall {
  /** The request's `request_id` — echo it back on the reply. */
  callID: string;
  name: string;
  /** The worker that sent the invocation (bus-stamped `worker_id`). */
  callerID: string;
  /** Always a non-empty object so handlers can read/write safely. */
  args: Record<string, unknown>;
  traceID: string;
}
