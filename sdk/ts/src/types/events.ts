/**
 * Universal bus protocol event types.
 *
 * Every worker must use these exact names to interoperate — a worker
 * implementing a capability must publish/subscribe with the matching event
 * type. Worker-specific domain events (e.g. `reason.*`, `timer.*`) are not
 * listed here; they are defined by their owning worker.
 *
 * Mirrors `core/event` constants.
 */
export const EventType = {
  // Worker presence and lifecycle.
  WorkerReady: "worker.ready",
  WorkerGone: "worker.gone",
  WorkerDiscover: "worker.discover",
  WorkerInput: "worker.input",
  WorkerAbort: "worker.abort",

  // Worker meta invocation channels. Some extensions still register these
  // (their op/subject payloads discriminate); reason's own meta capabilities
  // use dedicated event types (see pkg/reason in the main repo).
  WorkerUpdate: "worker.update",
  WorkerQuery: "worker.query",

  // Request-response pairing convention: any invocation of a capability is
  // answered with one of these, echoing the request's `request_id`.
  // request.completed carries the result; request.failed / request.rejected
  // carry the error; request.progressed streams partial results;
  // request.cancel is sent by the caller to cancel a pending one.
  RequestCompleted: "request.completed",
  RequestFailed: "request.failed",
  RequestRejected: "request.rejected",
  RequestProgressed: "request.progressed",
  RequestCancel: "request.cancel",
} as const;

export type EventTypeName = (typeof EventType)[keyof typeof EventType];