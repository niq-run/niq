// Capability: how this worker announces and learns the capabilities on the bus,
// resolves them, and applies them. Everything here is about capability — what to
// advertise, what to collect, what a name or address resolves to, and the two
// ways the universe is applied: as the LLM's tool list and as bus invocations.
//
// OUTBOUND: BroadcastReady announces this worker's contract (to peers, and in
// full to itself) and BroadcastDiscover asks the fleet who is online. INBOUND:
// HandleWorkerReady / handleWorkerGone maintain the discovered universe
// (w.discovered), the single source of truth for what this worker can call. The
// resolution helpers read that universe; the tool-list builder and the
// invocation primitive apply it. The extension registry — what this worker
// itself declares and how it answers — lives in pkg/baseworker.
package reason

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strings"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/llm"
)

// DiscoveredCapability is a capability as seen on the bus: which worker declared it
// and the event it responds to. It is the unified unit the tool-list builder
// operates on — this worker's own contract (learned from its self-directed
// worker.ready) and every peer's presence broadcast are presented uniformly,
// distinguished only by Source.
//
// This is the bus-discovered universe, NOT the worker's own declared registry:
// BaseReasonWorker learns it by observing worker.ready / worker.gone, so it
// describes what the worker can SEE, not what it announces. The self-registry
// (Extension / Register) lives in pkg/baseworker.
type DiscoveredCapability struct {
	Source      string          // declaring worker ID (own ID for self)
	Event       event.EventType // the extension's own event type (send_message, context.compress, ...)
	Description string
	Parameters  map[string]any
	// Response is the declared shape of the reply (JSON Schema), when the
	// declaring worker published one — the response half of the request/response
	// pair. Carried so the model can plan how to parse a result before calling:
	// essential in PTC mode, where a script's logic depends on the expected
	// output.
	Response map[string]any
}

// discoveredCapabilities returns the unified capability universe: every
// capability this worker knows about, its own included, in one list. It is fed
// by every worker.ready announcement — the worker's own directed full contract
// and every peer's presence broadcast — so the builder sees one bus-derived
// view, no two-source assembly.
func (w *BaseReasonWorker) discoveredCapabilities() []DiscoveredCapability {
	return w.discovered
}

// HandleWorkerReady learns a worker's capabilities from its worker.ready
// announcement, feeding the discovered universe (w.discovered). Each ready
// event carries that worker's complete contract (the worker excludes itself
// from its own presence broadcast and sends its full contract to itself), so
// the source's previous view is replaced wholesale — which is also what lets a
// re-announcement update a worker's capabilities.
func (w *BaseReasonWorker) HandleWorkerReady(evt event.Event) {
	workerID := evt.WorkerId
	if workerID == "" {
		return
	}

	// A reason's ready lists the events it RESPONDS to, not callable tools: this
	// worker exposes its own capabilities to itself, but a peer reason's ready is
	// not treated as callable capabilities — its extensions (context.compress,
	// program_query, ...) are never surfaced as tools here. Infra workers
	// (workspace/timer/program/directory...) expose real tools and are kept.
	if workerID != w.ID() && isReasonFamilyWorkerType(evt.Payload["type"]) {
		return
	}

	// Re-announcement is idempotent: drop this worker's previous view.
	w.discovered = removeDiscoveredCapabilities(w.discovered, workerID)

	// Parse the capability contract (watch) into the discovered universe. The
	// wire entry is {"event", "desc"?, "parameters"?}; the event type is the
	// capability's identity. This is the handler's last step, so a missing or
	// malformed watch just ends it.
	raw, ok := evt.Payload["watch"]
	if !ok {
		return
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return
	}
	var watch []map[string]any
	if err := json.Unmarshal(b, &watch); err != nil {
		return
	}
	for _, e := range watch {
		typ, _ := e["event"].(string)
		if typ == "" {
			continue
		}
		dc := DiscoveredCapability{Source: workerID, Event: event.EventType(typ)}
		dc.Description, _ = e["desc"].(string)
		dc.Parameters, _ = e["parameters"].(map[string]any)
		dc.Response, _ = e["response"].(map[string]any)
		w.discovered = append(w.discovered, dc)
	}
	if len(watch) > 0 {
		log.Printf("[reason %s] received watch from %s (%d entries)", w.ID(), workerID, len(watch))
	}
}

// removeDiscoveredCapabilities returns caps without the entries declared by source.
func removeDiscoveredCapabilities(caps []DiscoveredCapability, source string) []DiscoveredCapability {
	out := caps[:0]
	for _, c := range caps {
		if c.Source != source {
			out = append(out, c)
		}
	}
	return out
}

// handleWorkerGone forgets a departed worker's capabilities.
func (w *BaseReasonWorker) handleWorkerGone(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)
	if workerID == "" {
		return
	}

	w.discovered = removeDiscoveredCapabilities(w.discovered, workerID)
	log.Printf("[reason %s] forgot capabilities from %s", w.ID(), workerID)
}

// findProvider returns the peer worker providing the capability with the given
// bare event name (e.g. "elapse"), or false if none does. Own capabilities never
// qualify. Callers must hold w.mu. It is how the mechanism reaches a peer's
// capability by name rather than by a hardcoded worker id.
func (w *BaseReasonWorker) findProvider(capabilityEvent string) (string, bool) {
	for _, cap := range w.discovered {
		if cap.Source != w.ID() && string(cap.Event) == capabilityEvent {
			return cap.Source, true
		}
	}
	return "", false
}

// --- capability resolution --------------------------------------------------
//
// Reading the discovered universe: map a capability to its LLM-facing name, and
// resolve a name or address (source + event) back to a capability.

// toolNameOfCapability is the LLM-facing name of a discovered capability: an own
// capability is its event type with dots → underscores; a peer capability is
// provider__eventtype. It is the single naming used by both the tool-list
// builder and the reverse lookup (capabilityByToolName), so routing always
// agrees with the list. Invariant: worker IDs and tool names must not contain
// "__" (double underscore); '__' is the separator.
func toolNameOfCapability(w *BaseReasonWorker, cap DiscoveredCapability) string {
	n := strings.ReplaceAll(string(cap.Event), ".", "_")
	if cap.Source == w.ID() {
		return n
	}
	return cap.Source + "__" + n
}

// capabilityByToolName finds a discovered capability by its LLM-facing tool
// name. Used to route LLM tool calls: the cap carries the owning Source and
// the event type to send. Own capabilities route back to self; peer
// capabilities go to their Source — both invoked by the cap's own event.
func (w *BaseReasonWorker) capabilityByToolName(name string) (DiscoveredCapability, bool) {
	for _, cap := range w.discoveredCapabilities() {
		if toolNameOfCapability(w, cap) == name {
			return cap, true
		}
	}
	return DiscoveredCapability{}, false
}

// capabilityBySourceEvent finds a discovered capability addressed as (source
// worker id, bare event type). It backs the "request" tool: the model names the
// target worker and the capability event it wants invoked. Refuses peer-reason
// capabilities (not callable tools to this worker).
func (w *BaseReasonWorker) capabilityBySourceEvent(source, eventType string) (DiscoveredCapability, bool) {
	for _, cap := range w.discoveredCapabilities() {
		if cap.Source == source && string(cap.Event) == eventType && cap.Source != w.ID() {
			return cap, true
		}
	}
	return DiscoveredCapability{}, false
}

// isRequestCap reports whether a capability is THIS worker's own "request"
// tool (a reason worker's directed-call escape hatch). Only the bare name
// "request" on this reason worker qualifies — a peer reason's request is
// encoded provider__request and never treated as this escape hatch.
func (w *BaseReasonWorker) isRequestCap(cap DiscoveredCapability) bool {
	return cap.Source == w.ID() && string(cap.Event) == string(TypeRequest)
}

// --- convention capabilities -------------------------------------------------
//
// The small, fixed set of capabilities the mechanism itself depends on. Unlike
// the discovered universe (what the LLM may call), these names are conventions,
// not discoveries — but their target is decided per capability:
//
//	context.compress  self  — shrink THIS worker's own context (invokeContextCompress)
//	elapse            peer  — a reminder service (invokeElapseReminder)
//	timeout           peer  — armed by the LLM's own call, only recognized here
//
// (A model may also invoke a peer's context.compress as an ordinary discovered
// capability — that goes through the tool/request path, not this section.)
const (
	TypeContextCompress event.EventType = "context.compress"

	// The timer's fire event and the payload field naming its timer — the
	// timeout/reminder contract with the timer worker.
	TypeTimerTimeout event.EventType = "timer.timeout"
)

// Bare names recognized by convention, not event types — elapse resolves to a
// peer for reminders, timeout arms the round's tool-call timeout, and
// timerIDField names the timer in the fire payload. Deliberately untyped: they
// are used both as plain strings and (elapse) as an event type.
const (
	elapseTool   = "elapse"
	timeoutTool  = "timeout"
	timerIDField = "timer_id"
)

// invokeCapability puts one capability invocation on the bus: the capability's
// own event type, caller = this worker, directed to target, echoing requestID
// (empty = notification) and traceID. The single place a capability invocation
// is sent — the LLM's dispatched tool calls (handleToolCalls), invokeContextCompress
// and invokeElapseReminder all funnel through it.
func (w *BaseReasonWorker) invokeCapability(ctx context.Context, eventType event.EventType, target string, args map[string]any, requestID, traceID string) {
	evt := event.New(eventType, w.ID(), args)
	evt.RequestId = requestID
	evt.TraceID = traceID
	_ = w.Channel.Send(ctx, evt, target)
}

// invokeContextCompress is the self convention: ask THIS worker (through the bus,
// for audit) to shrink its own context. It is the single path every compress
// trigger (hard budget, ErrorContextLength) converges on; the worker's own
// handler implements the shrink and does its own bookkeeping.
func (w *BaseReasonWorker) invokeContextCompress(ctx context.Context) {
	w.invokeCapability(ctx, TypeContextCompress, w.ID(), nil, "", w.currentTraceID)
}

// invokeElapseReminder is the service convention: ask whichever peer offers the
// "elapse" capability to fire a reminder after durationMS, carrying purpose.
// Fire-and-forget (nothing is tracked); absent a provider, nothing is scheduled
// — logged, not fatal.
func (w *BaseReasonWorker) invokeElapseReminder(durationMS int64, purpose, requestID, traceID string) {
	w.mu.Lock()
	provider, ok := w.findProvider(elapseTool)
	w.mu.Unlock()
	if !ok {
		log.Printf("[reason %s] no peer offers the %q capability; reminder not scheduled", w.ID(), elapseTool)
		return
	}
	w.invokeCapability(context.Background(), elapseTool, provider, map[string]any{
		"duration_ms": durationMS,
		"purpose":     purpose,
	}, requestID, traceID)
}

// --- outbound presence ------------------------------------------------------

// BroadcastReady announces this worker's presence in two ready events:
//
//   - a broadcast to every peer except this worker (ExcludeWorkerID, via
//     baseworker.AnnounceReady) carrying the externally callable contract,
//     SelfOnly extensions left out, and
//   - a directed announcement to this worker carrying the full contract.
//
// Because the worker is excluded from its own broadcast, it receives exactly
// one self-sourced ready event, which is its complete view — so
// HandleWorkerReady can treat each ready event as that worker's whole contract
// and replace it wholesale on re-announcement (no merge, no local seeding).
// Exported for embedding workers and tests.
func (w *BaseReasonWorker) BroadcastReady() {
	// First batch: presence to every peer except this worker. Carries the
	// externally callable contract only — SelfOnly extensions are left out, so
	// peers never see them as callable tools. This is the generic announce
	// every worker uses; only the self-directed half below is reason-specific.
	w.AnnounceReady(w.workerType, nil)

	// Second batch: the full contract (SelfOnly included) sent to itself. This
	// is the self-directed round-trip through discovery — HandleWorkerReady
	// treats it like any peer's announcement, so this worker's own capabilities
	// flow through the same unified discovery pipeline as everyone else's.
	self := event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"type": w.workerType,
		// Full contract, SelfOnly included (this worker's own view).
		"watch": w.WatchEntries(true),
	})
	self.Transient = true // presence: delivered live, not durable history
	_ = w.Channel.Send(context.Background(), self, w.ID())
}

// BroadcastDiscover asks the fleet who is online: a worker.discover broadcast
// makes every already-started worker reply DIRECTLY with its ready (see
// process.go), so this worker learns those that predate it. Paired with
// BroadcastReady — its ready reaches whoever is already up, its discover makes
// those workers answer — a starting worker discloses both directions.
func (w *BaseReasonWorker) BroadcastDiscover() {
	disc := event.New(event.TypeWorkerDiscover, w.ID(), map[string]any{
		"worker_id": w.ID(),
	})
	disc.Transient = true // presence: live-only, not durable history
	_ = w.Channel.Broadcast(context.Background(), disc)
}

// --- applying capabilities: the LLM tool surface ----------------------------
//
// The discovered universe is *applied* two ways. First, as the LLM's tool list:
// ToolListBuilder is the policy turning capabilities into the tools the model is
// offered, and each chosen call routes back to its capability through the
// resolution helpers above. (The second — putting a resolved call on the bus as
// an invocation — is invokeCapability, in the convention section; the worker's
// own generated self tools, `message` / `request`, live with the other own event
// types in reason.go.)

// ToolListBuilder produces the LLM tool list from the discovered capability
// universe. Extensions carry no exposure marks — the builder decides, by its
// own policy, which capabilities the LLM sees and under what names. This is an
// extension point: a reason-family worker replaces it to control exactly what
// its LLM can call.
type ToolListBuilder func(w *BaseReasonWorker, caps []DiscoveredCapability) []llm.ToolDef

// defaultToolListBuilder is the default policy producing the LLM tool list from
// the discovered capability universe: every own capability is exposed under its
// event-type name (provider.* included — the worker may know and manage its own
// model supplier), and every peer capability under provider__name. Any
// capability is invoked by its own event type — the builder's policy decides
// what the LLM sees. Custom builders replace this.
func defaultToolListBuilder(w *BaseReasonWorker, caps []DiscoveredCapability) []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(caps))
	for _, cap := range caps {
		defs = append(defs, llm.ToolDef{
			Name:        toolNameOfCapability(w, cap),
			Description: withResponseHint(cap.Description, cap.Response),
			Parameters:  cap.Parameters,
		})
	}
	// Sort by name so the tool list is byte-identical across re-discovery: the
	// capability universe is filled in worker.ready arrival order, which is not
	// stable across a restart, and the tools sit at the front of every request —
	// a reordered list busts the provider's prefix cache on the first round.
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// responseHintMaxBytes caps the schema text appended to a tool description. The
// hint is there to tell the model what shape to expect, not to be a full
// reference: an enormous schema would bloat every request's tool list, and the
// authoritative copy is the directory worker's get_worker_info.
const responseHintMaxBytes = 600

// withResponseHint appends the declared reply shape to a tool description, so the
// model knows what it will get back. Tool definitions have no response-schema
// field in the function-calling APIs, so the description is where it goes;
// capabilities that declare nothing keep their description untouched.
func withResponseHint(desc string, response map[string]any) string {
	if len(response) == 0 {
		return desc
	}
	// A bare {"type":"string"} says nothing a caller could not assume, and the
	// hint is paid for in every request's tool list: skip it. A string WITH a
	// description (an output convention) is worth carrying.
	if len(response) == 1 && response["type"] == "string" {
		return desc
	}
	b, err := json.Marshal(response)
	if err != nil {
		return desc
	}
	hint := string(b)
	if len(hint) > responseHintMaxBytes {
		hint = hint[:responseHintMaxBytes] + "…"
	}
	if desc == "" {
		return "Response (JSON Schema): " + hint
	}
	return desc + "\n\nResponse (JSON Schema): " + hint
}

// DefaultToolListBuilder is the default ToolListBuilder policy (all discovered
// capabilities, own and peer, sorted by name for a cache-stable list). Exported
// so a reason-family worker can compose it over a filtered universe (e.g. keep
// only self + the program worker) instead of reimplementing the naming/sorting.
var DefaultToolListBuilder ToolListBuilder = defaultToolListBuilder

// ToolNameOfCapability is the LLM-facing name of a discovered capability (own:
// event type with dots→underscores; peer: provider__event). Exported so a custom
// ToolListBuilder can name the defs it emits.
func ToolNameOfCapability(w *BaseReasonWorker, cap DiscoveredCapability) string {
	return toolNameOfCapability(w, cap)
}

// LLMToolDefs builds the LLM tool list via the worker's tool-list builder
// over the discovered capability universe. Called at the start of every
// reasoning round.
func (w *BaseReasonWorker) LLMToolDefs() []llm.ToolDef {
	return w.toolListBuilder(w, w.discoveredCapabilities())
}

// isReasonFamilyWorkerType reports whether an announced worker type is a
// reason-family worker (the generic reason worker or the coding worker). A
// reason-family worker's ready lists the events IT responds to, not callable
// capabilities for peers, so the discovery path ignores it as a tool source.
func isReasonFamilyWorkerType(v any) bool {
	switch v {
	case "reason", "coding":
		return true
	}
	return false
}
