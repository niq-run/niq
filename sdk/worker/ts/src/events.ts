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

  // Tool invocation lifecycle.
  ToolRequested: "tool.request",
  ToolCancel: "tool.cancel",
  ToolCompleted: "tool.completed",
  ToolFailed: "tool.failed",
  ToolRejected: "tool.rejected",
  ToolPartial: "tool.partial",
} as const;

export type EventTypeName = (typeof EventType)[keyof typeof EventType];