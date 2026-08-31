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
	"github.com/niq-run/niq/pkg/baseworker"
	"github.com/niq-run/niq/pkg/reason/requesttracker"
	"github.com/niq-run/niq/pkg/reason/transcript"
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
	Transcript      transcript.Transcript

	Provider        llm.LLMProvider
	ProviderSources ProviderSources
	// ProviderName / ProviderModel record the initially active provider for
	// status reporting (worker.status op=providers carries the current choice).
	// Empty when unknown.
	ProviderName    string
	ProviderModel   string
	ReasoningEffort *string

	ContextWindow int
	BudgetSoft    float64
	BudgetHard    float64
	KeepTail      int

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
		cfg.Transcript = transcript.NewAccumulateTranscript(transcript.WithMaxPayloadBytes(cfg.MaxPayloadBytes))
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
		BaseWorker:      baseworker.NewBaseWorker(cfg.ID, cfg.Subscriptions, cfg.Bus),
		llmProvider:     initialProvider,
		providerSources: cfg.ProviderSources,
		providerName:    cfg.ProviderName,
		providerModel:   cfg.ProviderModel,
		transcript:      cfg.Transcript,
		tools:           make(map[string]worker.Tool),
		publishMap:      make(map[string][]EventPublish),
		toolListBuilder: defaultToolListBuilder,
		toolNameMap:     make(map[string]string),
		eventConverters: cfg.EventConverters,
		programs:        cfg.Programs,
		requestTracker:  requesttracker.NewRequestTracker(),
		contextWindow:   cfg.ContextWindow,
		budgetSoft:      cfg.BudgetSoft,
		budgetHard:      cfg.BudgetHard,
		keepTail:        cfg.KeepTail,
		reasoningEffort: cfg.ReasoningEffort,
	}
	if len(cfg.SeedMessages) > 0 {
		w.transcript.Apply(transcript.InputPatch{Messages: cfg.SeedMessages})
	}

	// Register the base extensions (provider switch/status). The extension
	// registry is the extension point; reason-family workers call Register to
	// add or replace extensions, and the default worker (pkg/worker/reason)
	// adds its own toolkit on top.
	w.registerBaseExtensions()

	// Resolve the context window for the selected provider/model so the budget
	// thresholds use the real size (API-reported or configured) instead of the
	// manual fallback (0 = disabled).
	w.resolveContextWindow()

	return w
}

// ── Mechanism accessors for embedding workers ───────────────────────────────
//
// These expose the mechanism's operations to reason-family workers so their
// extension handlers can be implemented outside pkg/reason without reaching
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
func (w *BaseReasonWorker) Transcript() transcript.Transcript { return w.transcript }

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

// ExtensionEntries returns the full watch announcement (every extension, SelfOnly
// included) for this worker — the directed self-contract handed to
// handleWorkerReady. Exported so embedding workers can build their own
// announcement; the test package uses it to assert the LLM-facing contract.
func (w *BaseReasonWorker) ExtensionEntries() []map[string]any { return w.extensionEntries() }

// LLMToolDefs returns the tool definitions exposed to the LLM (this worker's
// own extensions, learned from its directed full-contract announcement).
func (w *BaseReasonWorker) LLMToolDefs() []llm.ToolDef { return w.llmToolDefs() }

// ExtensionByToolName looks up a registered extension by its LLM-facing tool
// name. Exported for inspection by embedding workers and tests.
func (w *BaseReasonWorker) ExtensionByToolName(name string) (baseworker.Extension, bool) {
	return w.extensionByToolName(name)
}

// MetaExtensionCall reports whether msg contains a call to a meta extension
// (a registered extension whose event is not tool.request) and returns that
// block. Exported for embedding workers and tests.
func (w *BaseReasonWorker) MetaExtensionCall(msg llm.Message) (llm.ContentBlock, bool) {
	return w.metaExtensionCall(msg)
}

// StripToolCalls returns a copy of msg with all tool-call blocks removed,
// keeping thinking/text. Exported for embedding workers and tests.
func StripToolCalls(msg llm.Message) llm.Message { return stripToolCalls(msg) }

// HandleWorkerReady applies a peer (or self) worker.ready announcement,
// replacing that worker's whole contract. Exported for embedding workers and
// tests.
func (w *BaseReasonWorker) HandleWorkerReady(evt event.Event) { w.handleWorkerReady(evt) }

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

// watch is the single event loop goroutine. It blocks on busCh waiting
// for events, calls process() to handle them, and then calls tryReason()
// which starts reasoning when needReason is set and no reasoning is running.
func (w *BaseReasonWorker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(ctx, evt)
		case <-ctx.Done():
			return
		}
		w.tryReason(ctx)
	}
}

// tryReason is the decision gate. It is called after every process() in the
// watch loop, and at the end of every reason(). If needReason is set and no
// reasoning is running, it spawns a reasoning round on its own goroutine so
// the watch event loop stays responsive while the LLM call is in flight.
func (w *BaseReasonWorker) tryReason(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.needReason || w.isReasoning {
		return
	}

	w.isReasoning = true
	w.needReason = false

	go w.reason(ctx)
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
// workers: it embeds baseworker.BaseWorker (id/subs/channel) and owns the working
// notes, the LLM call, budget, tool table and the run flags of one reasoning
// round. It knows how to reason, not what to attend to.
type BaseReasonWorker struct {
	baseworker.BaseWorker
	mu sync.Mutex

	llmProvider     llm.LLMProvider
	providerSources ProviderSources
	providerName    string // active provider name (status reporting)
	providerModel   string // active provider model (status reporting)
	transcript      transcript.Transcript
	tools           map[string]worker.Tool // tools from the bus + built-ins; read by dispatch
	discovered      []DiscoveredCapability // unified capability universe (bus announcements only; the self-ready round-trip includes this worker's own capabilities)
	toolListBuilder ToolListBuilder        // LLM tool list policy (extension point)
	programs        []program.Program
	publishMap      map[string][]EventPublish // worker ID -> published events
	toolNameMap     map[string]string         // encoded LLM-facing name -> original declared name
	requestTracker  *requesttracker.RequestTracker
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
	activeTimeout           string                      // current round's set_tool_timeout call_id, "" if none
	activeTimeoutProvider   string                      // worker that set the active timeout (set time), "" if none
	interruptReason         requesttracker.PreemptCause // why the current reasoning round was interrupted
	immediateReasoningCause requesttracker.PreemptCause // why the next reasoning round was triggered
	currentTraceID          string

	// Rate-limit backoff state (429 handling); guarded by w.mu
	rateLimitAttempts int // consecutive 429s consumed in the current backoff chain

	cancelReason context.CancelFunc
	cancelRun    context.CancelFunc

	// Context budget state; guarded by w.mu
	contextWindow   int
	budgetSoft      float64
	budgetHard      float64
	keepTail        int
	lastUsageTokens int
	budgetReminded  bool
}
