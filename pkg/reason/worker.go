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
	"fmt"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/core/program"
	"github.com/54c1/niq/core/worker"
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

	// Compactor is how this worker compresses its transcript under window
	// pressure. nil uses the default LLM-summary compactor. It may also carry
	// extra operations (e.g. Rotate) reached by the tools that declare them.
	Compactor Compactor

	ContextWindow    int
	BudgetSoft       float64
	BudgetHard       float64
	KeepTail         int
	CompactDirective string

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
		compactDirectiveOverride: cfg.CompactDirective,
		reasoningEffort:          cfg.ReasoningEffort,
	}
	if len(cfg.SeedMessages) > 0 {
		w.transcript.Apply(InputPatch{Messages: cfg.SeedMessages})
	}

	// Compactor: default LLM-summary compactor unless supplied. It needs the
	// provider for summarization and the tail policy.
	if cfg.Compactor == nil {
		w.compactor = NewDefaultCompactor(initialProvider, cfg.KeepTail)
	} else {
		w.compactor = cfg.Compactor
	}

	// Register the core capabilities (tools + context meta ops + provider
	// switch/status). The capability registry is the extension point;
	// reason-family workers call Register to add or replace capabilities.
	w.registerCoreCapabilities()
	// Seed the discovery universe with the worker's own capabilities.
	w.seedOwnCapabilities()

	// Resolve the context window for the selected provider/model so the budget
	// thresholds use the real size (API-reported or configured) instead of the
	// manual fallback (0 = disabled).
	w.resolveContextWindow()

	return w
}

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
// broadcastReady announces this worker's presence on the bus, then publishes a
// second worker.ready directed to itself carrying this worker's own tool and
// meta-tool declarations. Reason discovers its own tools from that directed
// announcement (via handleWorkerReady), the same way it discovers any other
// worker's - so its built-in tools are not a special hardcoded set but a
// self-published declaration.
func (w *BaseReasonWorker) broadcastReady() {
	// Batch 1: broadcast - announce presence and the worker's capability
	// contract (watch: events it responds to). Peers / hiw discover the
	// meta capabilities (worker.update / worker.query) from
	// this declaration.
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
		"watch":     w.watchDeclarations(),
	}))

	// Batch 2: directed to self - declare this worker's own capability
	// contract (watch). Reason discovers its own capabilities from that
	// directed announcement (via handleWorkerReady), the same way it discovers
	// any other worker's.
	_ = w.Channel.Send(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
		"watch":     w.watchDeclarations(),
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

// Snapshot captures the worker's durable execution state (the transcript).
func (w *BaseReasonWorker) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.transcript.State()
}

// Restore rehydrates the worker from a Snapshot blob, restoring the reasoning
//
//	Called after construction and before Start.
func (w *BaseReasonWorker) Restore(state []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.transcript.Restore(state)
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
	discovered      []DiscoveredCap                 // unified capability universe (own seeded + bus announcements)
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

	compactor       Compactor
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
	compactDirectiveOverride string
	lastUsageTokens          int
	budgetReminded           bool
}
