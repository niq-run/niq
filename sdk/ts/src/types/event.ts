/** Lifecycle stage of an event. Mirrors `core/event.EventStatus`. */
export type EventStatus = "created" | "routed" | "delivered";

/**
 * Subscription declaration: an event of a type, optionally from a specific
 * source worker. Mirrors `core/event.EventPattern`.
 *
 * `type` supports exact match, `"*"` (any), and `"Prefix.*"` (prefix wildcard —
 * also matches the bare prefix, e.g. `"github.*"` matches `"github"` and
 * `"github.issue.new"`). An empty `type` matches nothing (fails closed).
 */
export interface EventPattern {
  type: string;
  source_id?: string;
}

/**
 * The core data unit of the niq event bus. Field names are the wire names
 * (`snake_case`) and must match `core/event.Event` exactly.
 *
 * Note on authorship: the bus overwrites `worker_id` on receipt (anti-spoofing).
 * The worker MUST supply `id` and `timestamp` — the bus does not fill them.
 * Use {@link createEvent} to get these right.
 */
export interface Event {
  id: string;
  type: string;
  status: EventStatus;
  payload: Record<string, unknown>;
  worker_id: string;
  target_worker_id?: string;
  /**
   * Pairs a request with its response: the request carries it, and the
   * response echoes it back (JSON-RPC id semantics). Empty means the event
   * is a notification — no response is expected. Mirrors `Event.RequestId`.
   */
  request_id?: string;
  trace_id?: string;
  specversion?: string;
  dataschema?: string;
  timestamp: number;
  recipients?: string[];
  /**
   * Names a worker the sender does not want to receive this event even if it
   * matches broadcast subscriptions; the bus skips it when routing broadcasts
   * (ignored for directed sends). Mirrors `Event.ExcludeWorkerID`.
   */
  exclude_worker_id?: string;
  /**
   * Marks live-streaming UI transients (streaming deltas, partial tool
   * output). The bus delivers them live but skips them on persistence.
   * Mirrors `Event.Transient`.
   */
  transient?: boolean;
}
