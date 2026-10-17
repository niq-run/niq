import type { Event, EventStatus } from "./types/index.js";
import { newId } from "./id.js";

/**
 * Create a new Event with defaults, mirroring `core/event.New`.
 *
 * The bus overwrites `worker_id` on receipt (anti-spoofing, per design D8), so
 * it is intentionally not set here — the client stamps it during publish. The
 * caller supplies `type` and `payload`; everything else is deterministic.
 */
export function createEvent(
  type: string,
  payload: Record<string, unknown> = {},
  now = Date.now(),
): Event {
  return {
    id: newId(now),
    type,
    status: "created" as EventStatus,
    payload,
    worker_id: "", // stamped by the client at publish time
    timestamp: Math.floor(now / 1000), // unix seconds, mirrors Go
    specversion: "niq/1.0",
  };
}