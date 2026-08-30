// Package reason provides the reasoning mechanism shared by reason-family
// workers. It is the machinery a reasoning node needs in one piece: the
// reasoning round (an LLM call + lifecycle broadcasts), the transcript, budget
// and compaction, tool-call tracking and dispatch, and system-prompt
// construction.
//
// This package deliberately contains no notion of *what* the worker is
// attending to. The split is:
//   - mechanism (here): how a reasoning node reasons — invokes the LLM, manages
//     its working notes, shrinks them against the finite window, dispatches
//     tools, renders its system prompt. Any reasoning node needs these, no
//     matter what it is pointed at.
//   - attention (the embedding worker): what the node subscribes to, what its
//     built-in tools are, what its event converters produce, what its programs
//     tell it to preserve when compacting. These reflect a specific goal.
//
// An embedding worker composes one goal-specific attention onto this shared
// mechanism.
package reason

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/core/program"
	"github.com/niq-run/niq/core/worker"
)

// EventConverter pairs an event pattern with a conversion function that
// transforms matching events into LLM messages. The embedding worker supplies
// its own converters (which events become which input to the model).
type EventConverter struct {
	Pattern   event.EventPattern
	Converter func(evt event.Event) []llm.Message
}

// EventPublish describes an event type that a worker publishes on the bus.
type EventPublish struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

const (
	DefaultBudgetSoft = 0.85
	DefaultBudgetHard = 0.97
	DefaultKeepTail   = 8
)

// Config holds the inputs to BaseReasonWorker — the pieces a reasoning node
// needs. The embedding worker assembles a Config (its goals, programs,
// converters, transcript) and calls NewBaseReasonWorker.
type Config struct {
	ID              string
	Bus             corebus.WorkerSideChannel
	Subscriptions   []event.EventPattern
	Programs        []program.Program
	EventConverters []EventConverter
	Transcript      Transcript

	Provider        llm.LLMProvider
	ProviderSources ProviderSources
	// ProviderName / ProviderModel record the initially active provider for
	// status reporting (worker.status op=providers carries the current choice).
	// Empty when unknown.
	ProviderName    string
	ProviderModel   string
	ReasoningEffort *string

	ContextWindow    int
	BudgetSoft       float64
	BudgetHard       float64
	KeepTail         int

	// MaxPayloadBytes caps a single text payload (tool result, external input
	// message) folded into the transcript; oversized payloads are truncated to
	// the head with a truncation note. 0 uses the default. Only applies to the
	// default AccumulateTranscript; a custom Transcript implements its own cap.
	MaxPayloadBytes int

	SeedMessages []llm.Message
}

// NewBaseReasonWorker assembles a BaseReasonWorker from a Config: applies
// budget defaults, initializes the empty tool table / publish map / tool-call
// tracker, and seeds the transcript with any handover brief.
func NewBaseReasonWorker(cfg Config) *BaseReasonWorker {
	if cfg.Transcript == nil {
		cfg.Transcript = NewAccumulateTranscript(WithMaxPayloadBytes(cfg.MaxPayloadBytes))
	}
	if cfg.BudgetSoft <= 0 {
		cfg.BudgetSoft = DefaultBudgetSoft
	}
	if cfg.BudgetHard <= 0 {
		cfg.BudgetHard = DefaultBudgetHard
	}
	if cfg.KeepTail <= 0 {
		cfg.KeepTail = DefaultKeepTail
	}
	if cfg.ReasoningEffort == nil {
		d := "medium"
		cfg.ReasoningEffort = &d
	}

	// The initial provider is the explicit single Provider, falling back to
	// ProviderSources.Default() when a runtime-switchable source is configured.
	initialProvider := cfg.Provider
	if cfg.ProviderSources != nil {
		if d := cfg.ProviderSources.Default(); d != nil {
			initialProvider = d
		}
	}

	w := &BaseReasonWorker{
		BaseWorker:               worker.NewBaseWorker(cfg.ID, cfg.Subscriptions, cfg.Bus),
		llmProvider:              initialProvider,
		providerSources:          cfg.ProviderSources,
		providerName:             cfg.ProviderName,
		providerModel:            cfg.ProviderModel,
		transcript:               cfg.Transcript,
		tools:                    make(map[string]worker.Tool),
		publishMap:               make(map[string][]EventPublish),
		capabilities:             make(map[string]registeredCapability),
		toolListBuilder:          defaultToolListBuilder,
		toolNameMap:              make(map[string]string),
		eventConverters:          cfg.EventConverters,
		programs:                 cfg.Programs,
		toolCallTracker:          NewToolCallTracker(),
		contextWindow:            cfg.ContextWindow,
		budgetSoft:               cfg.BudgetSoft,
		budgetHard:               cfg.BudgetHard,
		keepTail:                 cfg.KeepTail,
		reasoningEffort:          cfg.ReasoningEffort,
	}
	if len(cfg.SeedMessages) > 0 {
		w.transcript.Apply(InputPatch{Messages: cfg.SeedMessages})
	}

	// Register the base capabilities (provider switch/status). The capability
	// registry is the extension point; reason-family workers call Register to
	// add or replace capabilities, and the default worker (pkg/worker/reason)
	// adds its own toolkit on top.
	w.registerBaseCapabilities()

	// Resolve the context window for the selected provider/model so the budget
	// thresholds use the real size (API-reported or configured) instead of the
	// manual fallback (0 = disabled).
	w.resolveContextWindow()

	return w
}

// ── Mechanism accessors for embedding workers ───────────────────────────────
//
// These expose the mechanism's operations to reason-family workers so their
// capability handlers can be implemented outside pkg/reason without reaching
// into unexported state. The default worker's toolkit (send_message,
// list_workers, context.compress/rotate) lives in pkg/worker/reason and is
// built on exactly these.

// LLMProvider returns the currently active LLM provider. A worker's context
// strategy reads it lazily each call (e.g. for summarization) so it follows
// provider switches without rebinding.
func (w *BaseReasonWorker) LLMProvider() llm.LLMProvider { return w.llmProvider }

// CurrentTraceID returns the trace id of the event currently being processed.
func (w *BaseReasonWorker) CurrentTraceID() string { return w.currentTraceID }

// Transcript returns the worker's transcript for in-place editing. The context
// strategy (e.g. the default worker's context.compress handler) rewrites it to
// shrink the context; the caller already holds w.mu or edits inside a
// BeginEdit..CommitEdit window.
func (w *BaseReasonWorker) Transcript() Transcript { return w.transcript }

// KeepTail returns how many recent messages the default context strategy keeps
// when compressing (the worker's own policy; the mechanism does not use it).
func (w *BaseReasonWorker) KeepTail() int { return w.keepTail }

// TryReason asks the mechanism to schedule the next reasoning round after the
// worker has finished a context edit. The worker drives this (it knows when
// its async edit completed); the mechanism only owns the scheduling.
func (w *BaseReasonWorker) TryReason(ctx context.Context) {
	w.mu.Lock()
	w.needReason = true
	w.mu.Unlock()
	w.tryReason(ctx)
}

// ReplyTool answers a tool.request with a completed or failed result.
func (w *BaseReasonWorker) ReplyTool(callID, name, callerID, result string, isErr bool) {
	typ := event.TypeToolCompleted
	payload := map[string]any{"call_id": callID, "name": name, "result": result}
	if isErr {
		typ = event.TypeToolFailed
		payload = map[string]any{"call_id": callID, "name": name, "error": result}
	}
	evt := event.New(typ, w.ID(), payload)
	evt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// WatchEntries returns the full watch announcement (every capability, SelfOnly
// included) for this worker — the directed self-contract handed to
// handleWorkerReady. Exported so embedding workers can build their own
// announcement; the test package uses it to assert the LLM-facing contract.
func (w *BaseReasonWorker) WatchEntries() []map[string]any { return w.watchEntries() }

// LLMToolDefs returns the tool definitions exposed to the LLM (this worker's
// own capabilities, learned from its directed full-contract announcement).
func (w *BaseReasonWorker) LLMToolDefs() []llm.ToolDef { return w.llmToolDefs() }

// CapabilityByToolName looks up a registered capability by its LLM-facing tool
// name. Exported for inspection by embedding workers and tests.
func (w *BaseReasonWorker) CapabilityByToolName(name string) (Capability, bool) {
	return w.capabilityByToolName(name)
}

// MetaCapabilityCall reports whether msg contains a call to a meta capability
// (a registered capability whose event is not tool.request) and returns that
// block. Exported for embedding workers and tests.
func (w *BaseReasonWorker) MetaCapabilityCall(msg llm.Message) (llm.ContentBlock, bool) {
	return w.metaCapabilityCall(msg)
}

// StripToolCalls returns a copy of msg with all tool-call blocks removed,
// keeping thinking/text. Exported for embedding workers and tests.
func StripToolCalls(msg llm.Message) llm.Message { return stripToolCalls(msg) }

// HandleWorkerReady applies a peer (or self) worker.ready announcement,
// replacing that worker's whole contract. Exported for embedding workers and
// tests.
func (w *BaseReasonWorker) HandleWorkerReady(evt event.Event) { w.handleWorkerReady(evt) }

// BroadcastReady re-announces this worker's presence (two-batch: peers then
// self). Exported for embedding workers and tests.
func (w *BaseReasonWorker) BroadcastReady() { w.broadcastReady() }

// Start begins the event watch. It returns an error if the worker is already
// started. Provided by the base so embedding reason-family workers inherit
// the lifecycle without reimplementing it.
func (w *BaseReasonWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return fmt.Errorf("reason %s: already started", w.ID())
	}

	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancelRun = cancelFn

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)

	w.broadcastReady()
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerDiscover, w.ID(), map[string]any{
		"worker_id": w.ID(),
	}))

	w.started = true
	return nil
}

// broadcastReady re-announces this worker's presence on the bus in response
// to a worker.discover. A worker-level identity action.
// broadcastReady announces this worker's presence in two ready events:
//
//   - a broadcast to every peer except this worker (ExcludeWorkerID) carrying
//     the externally callable contract, SelfOnly capabilities left out, and
//   - a directed announcement to this worker carrying the full contract.
//
// Because the worker is excluded from its own broadcast, it receives exactly
// one self-sourced ready event, which is its complete view — so
// handleWorkerReady can treat each ready event as that worker's whole contract
// and replace it wholesale on re-announcement (no merge, no local seeding).
func (w *BaseReasonWorker) broadcastReady() {
	peerWatch := make([]map[string]any, 0, len(w.capabilities))
	for _, rc := range w.capabilities {
		if rc.cap.SelfOnly {
			continue
		}
		peerWatch = append(peerWatch, w.watchEntry(rc.cap))
	}
	presence := event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
		"watch":     peerWatch,
	})
	presence.ExcludeWorkerID = w.ID()
	_ = w.Channel.Broadcast(context.Background(), presence)

	_ = w.Channel.Send(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
		"watch":     w.watchEntries(),
	}), w.ID())
}

// Stop cancels the worker's event watch.
func (w *BaseReasonWorker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.started {
		return nil
	}
	w.cancelRun()
	w.cancelRun = nil
	w.started = false

	return nil
}

// reasonState is the persisted runtime state of a reason worker.
//
// The transcript is carried as a raw blob because its shape belongs to the
// transcript, not to us. Provider is the provider/model the worker is currently
// using, so a worker switched at runtime (worker.update provider.switch)
// resumes on that choice instead of silently falling back to the configured
// default — without it, a switch would be lost on every restart.
type reasonState struct {
	Transcript json.RawMessage    `json:"transcript"`
	Provider   *providerSelection `json:"provider,omitempty"`
}

type providerSelection struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// Snapshot captures the worker's durable execution state: the transcript plus
// its current provider/model selection.
func (w *BaseReasonWorker) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	transcript, err := w.transcript.State()
	if err != nil {
		return nil, err
	}
	state := reasonState{Transcript: transcript}
	// Only persist a selection the worker actually made; a worker left on its
	// configured default stays on it (and picks up config changes to it).
	if w.providerName != "" && w.providerModel != "" {
		state.Provider = &providerSelection{Name: w.providerName, Model: w.providerModel}
	}
	return json.Marshal(state)
}

// Restore rehydrates the worker from a Snapshot blob, restoring the transcript
// and re-applying the persisted provider/model selection.
//
//	Called after construction and before Start.
func (w *BaseReasonWorker) Restore(state []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var s reasonState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("reason restore: %w", err)
	}
	if err := w.transcript.Restore(s.Transcript); err != nil {
		return err
	}
	// Re-applying the selection rebuilds the provider, so the worker really
	// uses it rather than only reporting it. A stale selection (the provider or
	// model is no longer configured) is not fatal: note it and stay on the
	// provider resolved at construction, which is always usable.
	if s.Provider != nil && s.Provider.Name != "" && s.Provider.Model != "" {
		if err := w.setActiveProvider(s.Provider.Name, s.Provider.Model); err != nil {
			log.Printf("[reason %s] restore: persisted provider %s model %s unavailable (%v); keeping %s model %s",
				w.ID(), s.Provider.Name, s.Provider.Model, err, w.providerName, w.providerModel)
			return nil
		}
		log.Printf("[reason %s] restore: resumed provider %s model %s", w.ID(), s.Provider.Name, s.Provider.Model)
	}
	return nil
}

// Messages returns the current working transcript for reading (e.g. the
// embedding worker observing its own state). Callers must not mutate it.
func (w *BaseReasonWorker) Messages() []llm.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.transcript.Render()
}

// BaseReasonWorker is the reasoning mechanism shared by all reason-family
// workers: it embeds worker.BaseWorker (id/subs/channel) and owns the working
// notes, the LLM call, budget, tool table and the run flags of one reasoning
// round. It knows how to reason, not what to attend to.
type BaseReasonWorker struct {
	worker.BaseWorker
	mu sync.Mutex

	llmProvider     llm.LLMProvider
	providerSources ProviderSources
	providerName    string // active provider name (status reporting)
	providerModel   string // active provider model (status reporting)
	transcript      Transcript
	tools           map[string]worker.Tool          // tools from the bus + built-ins; read by dispatch
	discovered      []DiscoveredCap                 // unified capability universe (bus announcements only; the self-ready round-trip includes this worker's own capabilities)
	capabilities    map[string]registeredCapability // capability registry (the extension point)
	toolListBuilder ToolListBuilder                 // LLM tool list policy (extension point)
	programs        []program.Program
	publishMap      map[string][]EventPublish // worker ID -> published events
	toolNameMap     map[string]string         // encoded LLM-facing name -> original declared name
	toolCallTracker *ToolCallTracker
	// toolCallSeq mints globally-unique tool-call ids for calls whose model
	// omitted one. It must be monotonic across rounds (not per-turn) because the
	// tracker persists pending calls between rounds — a per-turn "call_0" would
	// collide with a still-pending call_0 from a previous round and mispair.
	toolCallSeq uint64

	eventConverters []EventConverter

	reasoningEffort *string

	started                 bool
	needReason              bool
	isReasoning             bool
	activeTimeout           string       // current round's set_tool_timeout call_id, "" if none
	activeTimeoutProvider   string       // worker that set the active timeout (set time), "" if none
	interruptReason         PreemptCause // why the current reasoning round was interrupted
	immediateReasoningCause PreemptCause // why the next reasoning round was triggered
	currentTraceID          string

	// Rate-limit backoff state (429 handling); guarded by w.mu
	rateLimitAttempts int // consecutive 429s consumed in the current backoff chain

	cancelReason context.CancelFunc
	cancelRun    context.CancelFunc

	// Context budget state; guarded by w.mu
	contextWindow            int
	budgetSoft               float64
	budgetHard               float64
	keepTail                 int
	lastUsageTokens          int
	budgetReminded           bool
}
