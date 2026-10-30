package history

import (
	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// registerHelp declares worker.help on the history worker. Both events get
// static usage guides: the way a query is shaped does not depend on the
// worker's config, so params never changes the answer. The list guide carries
// the workflow (narrow a listing, then drill in with get); the get guide
// points back at list for discovery.
func (w *Worker) registerHelp() {
	w.RegisterHelp(map[event.EventType]baseworker.HelpFunc{
		TypeHistoryList: staticHelp(listHelp),
		TypeHistoryGet:  staticHelp(getHelp),
	})
}

// staticHelp adapts a fixed usage guide to HelpFunc.
func staticHelp(text string) baseworker.HelpFunc {
	return func(map[string]any) (string, bool) { return text, true }
}

const listHelp = `# history.list

Recall PAST events from the event bus store (your own or another worker's)
across time and restarts. Use it only to recover or verify historical facts
your current context no longer holds — never for present state (the directory
worker answers that) or to re-read what is already in context.

` + "`" + `json
{"worker": "analyst", "types": ["bash", "request.completed"], "limit": 20}
` + "`" + `

## Arguments

All optional; narrow aggressively anyway — a small page is a good page.

- "worker" — worker id to filter by source, target, or recipient; "*" (default)
  means all workers.
- "role" — "sent" (the worker is the source) or "received" (target/recipient);
  omit for both.
- "since" — unix seconds; only events at or after this time.
- "types" — exact event types, one or many: ["bash", "request.completed"], or
  the single string "bash".
- "types" — array of exact event types; filtering happens in the store.
- "trace_id" / "request_id" — only events in this reasoning trace, or carrying
  this request id (a tool call and its reply echo it).
- "before" — event id anchor: only events strictly before it (newest first).
- "limit" — max events per call (default 50, hard-capped at the configured page
  size).
- "desc" — true (default) = newest first.
- "max_bytes" — per-event payload cap in bytes for this call, overriding the
  worker default.
- "truncate" — false returns every payload in full; use sparingly.

## Workflow

1. Narrow with worker + types + limit.
2. An omitted payload (payload_omitted) is a pointer: drill in with history.get.
3. Don't page through a whole history you only cursor-checked once — the store
   is append-only and live, and each page costs tokens.

The reply is {count, payload_omitted?, events[]}; each event carries id, type,
worker, ts, and payload as an OBJECT (or payload_omitted + payload_preview when
it did not fit). history.get returns the same record with the payload complete,
so one piece of code reads either reply.
`

const getHelp = `# history.get

Drill into the FULL content of specific historical events. Call it only after
history.list showed you an omitted payload or handed you an exact id or
request_id — it returns large payloads verbatim, so fetch only what you
genuinely need and cannot deduce from what you already have. The records are the
same shape history.list returns, payload included.

` + "`" + `json
{"id": "evt_01H..."}
` + "`" + `

## Arguments

Exactly one of:

- "id" — one event, in full (the id as shown by history.list).
- "request_id" — every event carrying that request id: the whole
  request→response pair (the tool call and whichever request.* reply echoed
  it).

Unknown ids and request ids fail the call. To browse or filter, use
history.list — that is the narrowing tool; this one is the drill-in.
`
