import type { Event } from "./event.js";

/**
 * WorkerSideChannel — the worker's view of a connection to the bus.
 *
 * Mirrors `core/bus.WorkerSideChannel`. Implementations hide whether the bus
 * is in-process, over HTTP, or behind a relay; the interface is the same.
 *
 * Semantics of Send vs Broadcast (deliberately distinct, not interchangeable):
 * - Send      — "I know who should handle this." Directed to explicit targets.
 *               targets must be non-empty (use Broadcast for undirected).
 * - Broadcast — "Whoever is interested handles it." Delivered to all online
 *               workers whose subscription matches. If no one subscribes, the
 *               event silently disappears — a valid end of the execution path.
 */
export interface WorkerSideChannel {
  /** Transport-level connection identifier (this worker's ID). */
  readonly id: string;

  /**
   * Establish the connection to the bus. No-op if already connected.
   * @throws if the transport rejects the connection (bad credential, etc.)
   */
  connect(): Promise<void>;

  /** Send a directed event to specific target workers. targets must be non-empty. */
  send(event: Event, ...targets: string[]): Promise<void>;

  /** Broadcast an event to all matching subscribers. */
  broadcast(event: Event): Promise<void>;

  /**
   * Iterate events routed to this worker (directed or broadcast).
   * The stream ends when the connection closes.
   */
  events(): AsyncIterable<Event>;

  /** Close the connection and release resources. */
  close(): Promise<void>;
}