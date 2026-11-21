// BUILTIN_EVENT_TYPES are the universal bus-protocol event types every worker
// must use verbatim (see niq/core/itfs/event/event.go). The allow-list editors
// offer them as one-click picks; the field itself stays free-text so
// worker-specific types (reason.*, timer.*, …) can still be typed by hand.
export const BUILTIN_EVENT_TYPES = [
  'worker.ready',
  'worker.gone',
  'worker.discover',
  'worker.input',
  'worker.abort',
  'worker.readme',
  'worker.help',
  'request.completed',
  'request.failed',
  'request.rejected',
  'request.progressed',
  'request.cancel',
  'approval.request',
  'approval.decision',
] as const
