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
	Source string // declaring worker ID (own ID for self)
	// SourceType is the declaring worker's announced type (reason, workspace,
	// directory, ...). Carried so a tool-list policy can select a whole worker
	// by what it IS rather than by its id or by enumerating its events.
	SourceType  string
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
	// program_attach, ...) are never surfaced as tools here. Infra workers
	// (workspace/timer/program/directory...) expose real tools and are kept.
	sourceType, _ := evt.Payload["type"].(string)
	if workerID != w.ID() && isReasonFamilyWorkerType(sourceType) {
		// A reason peer outside the collaboration scope is kept out of this
		// worker's world entirely: no discovered capabilities, nothing to call.
		// The id is remembered so a directed send (which does not need
		// discovery) is refused too.
		if w.reasonPeerOutOfScope(workerID) {
			w.markOutOfScope(workerID)
			w.discovered = removeDiscoveredCapabilities(w.discovered, workerID)
		}
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
		dc := DiscoveredCapability{Source: workerID, SourceType: sourceType, Event: event.EventType(typ)}
		dc.Description, _ = e["desc"].(string)
		dc.Parameters, _ = e["parameters"].(map[string]any)
		dc.Response, _ = e["response"].(map[string]any)
		w.discovered = append(w.discovered, dc)
	}
	if len(watch) > 0 {
		log.Printf("[reason %s] received watch from %s (%d entries)", w.ID(), workerID, len(watch))
	}
}

// reasonPeerOutOfScope reports whether a reason-family peer is outside this
// worker's collaboration scope. A nil scope means every peer is inside it.
func (w *BaseReasonWorker) reasonPeerOutOfScope(peerID string) bool {
	w.scopeMu.RLock()
	defer w.scopeMu.RUnlock()
	if w.collaborators == nil {
		return false
	}
	return !w.collaborators[peerID]
}

// markOutOfScope remembers a reason peer that announced outside the scope, so a
// directed send to it can be refused even though it is not in the discovered
// universe. Without this, an id learned some other way (a directory search, a
// handover brief) would be reachable.
func (w *BaseReasonWorker) markOutOfScope(peerID string) {
	w.scopeMu.Lock()
	w.excludedPeers[peerID] = true
	w.scopeMu.Unlock()
}

// MayAddress reports whether this worker may send a directed event to target.
// The only restriction is the collaboration scope: a reason-family peer that
// announced outside the whitelist is refused. Capability workers, unknown ids
// and this worker itself are always allowed — the scope governs who thinks with
// you, not which capabilities you may call.
func (w *BaseReasonWorker) MayAddress(target string) bool {
	if target == "" || target == w.ID() {
		return true
	}
	w.scopeMu.RLock()
	defer w.scopeMu.RUnlock()
	return !w.excludedPeers[target]
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

// isRequestCap reports whether a capability is THIS worker's own "request"
// tool (a reason worker's directed-call escape hatch). Only the bare name
// "request" on this reason worker qualifies — a peer reason's request is
// encoded provider__request and never treated as this escape hatch.
func (w *BaseReasonWorker) isRequestCap(cap DiscoveredCapability) bool {
	return cap.Source == w.ID() && string(cap.Event) == string(TypeRequest)
}

// isRunProgramCap reports whether a capability is THIS worker's own built-in
// run_program tool — the script counterpart of request.
func (w *BaseReasonWorker) isRunProgramCap(cap DiscoveredCapability) bool {
	return cap.Source == w.ID() && string(cap.Event) == string(TypeRunProgram)
}

// findProviderByType returns the peer of the given worker type that serves the
// given event. It resolves a built-in tool's target without hardcoding an id:
// run_program finds whichever worker announces itself as the program worker.
// Callers must hold w.mu.
func (w *BaseReasonWorker) findProviderByType(workerType string, evt event.EventType) (string, bool) {
	for _, cap := range w.discovered {
		if cap.Source != w.ID() && cap.SourceType == workerType && cap.Event == evt {
			return cap.Source, true
		}
	}
	return "", false
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
//     Internal extensions left out, and
//   - a directed announcement to this worker carrying the full contract.
//
// Because the worker is excluded from its own broadcast, it receives exactly
// one self-sourced ready event, which is its complete view — so
// HandleWorkerReady can treat each ready event as that worker's whole contract
// and replace it wholesale on re-announcement (no merge, no local seeding).
// Exported for embedding workers and tests.
func (w *BaseReasonWorker) BroadcastReady() {
	// First batch: presence to every peer except this worker. Carries the
	// externally advertised contract only — Internal extensions are left out, so
	// peers never see them in the catalogue. This is the generic announce every
	// worker uses; only the self-directed half below is reason-specific.
	w.AnnounceReady(w.workerType, nil)

	// Second batch: the full contract (Internal included) sent to itself. This
	// is the self-directed round-trip through discovery — HandleWorkerReady
	// treats it like any peer's announcement, so this worker's own capabilities
	// flow through the same unified discovery pipeline as everyone else's.
	self := event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"type": w.workerType,
		// Full contract, Internal included (this worker's own view).
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
// authoritative copy is the directory worker's describe.
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

// Worker types the reason-family scopes select by. Selecting a whole peer
// surface by TYPE rather than by id means a worker with a non-default id still
// resolves, and a new event on that worker needs no change here.
const (
	DirectoryWorkerType = "directory"
	ProgramWorkerType   = "program"
)

// IsMetaCapability reports whether an event describes a worker rather than does
// work: readme (read and replace) and per-event help. These are how a caller
// learns about a worker, not capabilities it invokes, so no scope can expose them
// as tools — a request reaches them when one is genuinely needed. The rule is
// global so a worker cannot leak its self-description into the model's tool list
// merely by being a type that an exposed policy takes whole.
func IsMetaCapability(t event.EventType) bool {
	switch t {
	case event.TypeWorkerReadme, event.TypeWorkerHelp, TypeReadmeUpdate:
		return true
	}
	return false
}

// ToolScope declares what a worker's model may see as tools. It is a filter over
// the discovered universe, not a naming rule: DefaultToolListBuilder still owns
// the names, the reply hints and the cache-stable sort.
//
// A capability is exposed iff it is not a meta capability and one of
//
//   - it is the worker's own and Own lists its event;
//   - the announcing worker's type is in PeerTypes (its whole surface);
//   - the announcing worker's type is in PeerEvents and that event is listed.
//
// Everything else stays reachable by request — from peers and from the model
// itself. Changing a worker's visible range is therefore a one-line edit where
// the worker is assembled, not a new builder.
type ToolScope struct {
	// Own lists this worker's own events that become tools. Anything else it
	// serves is request-only.
	Own []event.EventType
	// PeerTypes lists peer worker types whose whole announced surface becomes
	// tools. Empty exposes no peer tools, and no peer meta tools ever.
	PeerTypes []string
	// PeerEvents narrows a peer type to specific events, for the case where a
	// worker is the invocation channel but only one of its events should be
	// listed (PTC: the program worker contributes run and nothing else). A type
	// in PeerTypes contributes everything; a type here contributes exactly the
	// events named. Meta events are still dropped.
	PeerEvents map[string][]event.EventType
}

// DefaultToolScope is the reason family's scope, and it is the whole tool list:
// the three built-in sends and nothing else.
//
//   - message      — leave a note (no reply awaited)
//   - request      — send any event and await its reply
//   - run_program  — send events from inside a script (the program worker's run)
//
// Nothing peer-supplied is exposed. Discovery (the directory worker) is reached
// with request like any other capability, so the tool list is fixed: it does not
// change as workers come and go, and the model has exactly three ways to act.
var DefaultToolScope = ToolScope{
	Own: []event.EventType{TypeMessage, TypeRequest, TypeRunProgram},
}

// ScopedToolListBuilder turns a ToolScope into a ToolListBuilder.
func ScopedToolListBuilder(scope ToolScope) ToolListBuilder {
	own := make(map[event.EventType]bool, len(scope.Own))
	for _, t := range scope.Own {
		own[t] = true
	}
	peerTypes := make(map[string]bool, len(scope.PeerTypes))
	for _, t := range scope.PeerTypes {
		peerTypes[t] = true
	}
	peerEvents := make(map[string]map[event.EventType]bool, len(scope.PeerEvents))
	for t, evts := range scope.PeerEvents {
		set := make(map[event.EventType]bool, len(evts))
		for _, e := range evts {
			set[e] = true
		}
		peerEvents[t] = set
	}
	return func(w *BaseReasonWorker, caps []DiscoveredCapability) []llm.ToolDef {
		keep := make([]DiscoveredCapability, 0, len(caps))
		for _, c := range caps {
			if IsMetaCapability(c.Event) {
				continue
			}
			if c.Source == w.ID() {
				if own[c.Event] {
					keep = append(keep, c)
				}
				continue
			}
			if peerTypes[c.SourceType] {
				keep = append(keep, c)
				continue
			}
			if set := peerEvents[c.SourceType]; set != nil && set[c.Event] {
				keep = append(keep, c)
			}
		}
		return DefaultToolListBuilder(w, keep)
	}
}

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
