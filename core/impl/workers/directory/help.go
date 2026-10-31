package directory

import (
	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// registerHelp declares worker.help on the directory worker. Both events get a
// static guide: what they answer does not depend on the caller or on the
// worker's config, so params never changes the text. The guides carry what the
// single-line tool descriptions cannot — the two modes of search, how the type
// filters compose, and what describe is the authority FOR.
func (w *DirectoryWorker) registerHelp() {
	w.RegisterHelp(map[event.EventType]baseworker.HelpFunc{
		TypeSearch:   staticHelp(searchHelp),
		TypeDescribe: staticHelp(describeHelp),
	})
}

// staticHelp adapts a fixed guide to HelpFunc.
func staticHelp(text string) baseworker.HelpFunc {
	return func(map[string]any) (string, bool) { return text, true }
}

const searchHelp = `# search

The entry point to everyone else: this worker's whole business is telling you
who exists and what they answer. One event, two modes, chosen by whether you
pass a query.

## Mode 1 — the roster (no query)

` + "`" + `json
{"types": ["workspace", "program"]}
` + "`" + `


Every worker on the bus as worker_id + type, sorted by id. No schemas, no
published events — this is the cheap answer to "who is here", including peer
reason workers and this worker itself.

## Mode 2 — a keyword search (query given)

` + "`" + `json
{"query": "web search", "limit": 10}
` + "`" + `


Case-insensitive search over worker ids, types, and the tools/events each
worker exposes. Every whitespace-separated term must match somewhere, so more
terms narrow the result. Hits are slim — id, type, and where it matched — and
the matched field tells you WHY a worker surfaced.

## Arguments

- query — keywords. Omit for the roster.
- limit — max hits, query mode only (default 20, max 100). Ignored for the
  roster: the roster is never truncated, it is already minimal.
- types — only these worker types, e.g. ["workspace"]. Empty means any.
- exclude_types — drop these types, e.g. ["reason", "coding"] when you are
  looking for a capability and the other thinkers are noise.

types and exclude_types combine, and exclusion wins: a type in both lists is
dropped. Matching is case-insensitive either way.

## Reply

{query, total, workers[]}. total counts every match before limit; workers[] is
the page. query is "" and matched is absent in roster mode.

## After search

Search results are pointers, not contracts. describe the candidate you are
about to call before calling it.
`

const describeHelp = `# describe

One worker's contract: its type, the events it serves with complete argument
and reply schemas, and the events it publishes. This is the authoritative
answer to "what can I send it, and with what arguments" — everything else
(readme, help, guesswork) is secondary.

` + "`" + `json
{"worker": "workspace"}
` + "`" + `


## When to call it

- Before calling any worker you have not called before.
- On yourself: request(directory, "describe", {"worker": "<your own id>"}) lists
  the events you serve, which is how you learn what you can do to yourself —
  attach programs, switch provider, compress context, update readme.
- After a call fails in a way that looks like a wrong event name or wrong
  arguments: re-read the schema rather than guessing again.

## Arguments

- worker — the worker id, exactly as search returned it. A wrong id fails the
  call; the error names search, because search is where ids come from.

## Reply

{worker_id, type, tools[], publishes[]}. Each tools entry carries:

- event — the event name you send
- desc — what it does
- parameters — the argument schema (the object you pass as "arguments")
- response — the reply schema, when the worker declares one

publishes lists events the worker emits on its own (broadcasts, notices); they
are not things you can send it.

## Not the same as worker.readme

describe is the machine contract — names and schemas. worker.readme is the
worker's own account of what it is for and what it has been working on lately;
read that when you are deciding WHETHER to delegate, and describe when you have
decided and need to know HOW to call.
`
