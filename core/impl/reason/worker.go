// Package reason provides the reasoning mechanism shared by reason-family
// workers: the reasoning round (an LLM call + lifecycle broadcasts), event
// dispatch, the context-window pressure that fires the context.compress
// convention, capability discovery and announcement over the bus, provider
// management, and system-prompt construction.
//
// Two self-contained sub-domains live in subpackages: the transcript
// construction core (pkg/reason/transcript) and the outgoing request
// tracker (pkg/reason/requesttracker). The generic worker base and its
// extension registry live in pkg/baseworker.
//
// This package deliberately contains no notion of *what* the worker is
// attending to. The split is:
//   - mechanism (here): how a reasoning node reasons — invokes the LLM, manages
//     its working notes, watches the context window (emitting the
//     context.compress convention under pressure), discovers and dispatches
//     tools, renders its system prompt. Any reasoning node needs these, no
//     matter what it is pointed at.
//   - attention (the embedding worker): what the node subscribes to, what its
//     extensions are, what its event converters produce, what its programs
//     tell it to preserve when it shrinks its transcript. These reflect a
//     specific goal.
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

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/reason/requesttracker"
	"github.com/niq-run/niq/core/impl/reason/transcript"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/llm"
	"github.com/niq-run/niq/core/itfs/program"
)

// EventConverter pairs an event pattern with a conversion function that
// transforms matching events into LLM messages. The embedding worker supplies
// its own converters (which events become which input to the model).
type EventConverter struct {
	Pattern   event.EventPattern
	Converter func(evt event.Event) []llm.Message
}

const (
	DefaultBudgetSoft = 0.85
	DefaultBudgetHard = 0.97
	DefaultTailTokens = 10000
)

// Config holds the inputs to BaseReasonWorker — the pieces a reasoning node
// needs. The embedding worker assembles a Config (its goals, programs,
// converters, transcript) and calls NewBaseReasonWorker.
type Config struct {
	ID              string
	Bus             corebus.WorkerSideChannel
	Programs        []program.Program
	EventConverters []EventConverter
	Transcript      transcript.Transcript

	Provider        llm.LLMProvider
	ProviderSources ProviderSources
	// ProviderName / ProviderModel record the initially active provider for
	// status reporting (provider.current carries the current choice). Empty
	// when unknown.
	ProviderName    string
	ProviderModel   string
	ReasoningEffort *string

	// OnDurableChange, when set, is called after the worker changes state it
	// must survive a restart (a runtime provider switch), so the embedding
	// layer can persist it — the mechanism itself has no store. Signalling is
	// baseworker's: coalesced, and delivered off the event loop so the
	// callback is free to Snapshot() (see baseworker's durable.go).
	OnDurableChange func()

	BudgetSoft float64
	BudgetHard float64
	TailTokens int

	// MaxPayloadBytes caps a single text payload (tool result, external input
	// message) folded into the transcript; oversized payloads are truncated to
	// the head with a truncation note. 0 uses the default. Only applies to the
	// default AccumulateTranscript; a custom Transcript implements its own cap.
	MaxPayloadBytes int

	SeedMessages []llm.Message

	// Readme is this worker's own description — what it is responsible for and
	// what it has recently been working on. It is the seed of the worker's
	// readme (worker.readme); runtime updates by the worker itself live in its
	// snapshot and override this value on restore.
	Readme string

	// WorkerType is the type label this worker announces in its presence
	// (worker.ready payload, and the baseworker AnnounceReady type). Defaults to
	// "reason"; a reason-family worker (e.g. the coding worker) sets its own so
	// peers and the directory see the accurate type.
	WorkerType string

	// ToolListBuilder is the LLM tool-list policy (what the model sees). nil
	// uses DefaultToolListBuilder (all discovered capabilities). A reason-family
	// worker supplies its own to narrow the surface — e.g. a coding worker's
	// "ptc" mode exposing only the program worker's tools.
	ToolListBuilder ToolListBuilder

	// Collaborators, when non-nil, is the whitelist of reason-family workers
	// this worker may collaborate with. nil (the default) means every reason
	// worker is visible; an empty non-nil slice means none. A reason peer outside
	// the list is dropped at discovery, so this worker never learns it, and a
	// directed call to it is refused. Capability workers are unaffected: the
	// scope says who thinks with you, not which capabilities you may call.
	Collaborators []string
}

// NewBaseReasonWorker assembles a BaseReasonWorker from a Config: applies
// budget defaults, initializes the empty tool-call tracker, and seeds the
// transcript with any handover brief.
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
	if cfg.TailTokens <= 0 {
		cfg.TailTokens = DefaultTailTokens
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

	toolListBuilder := defaultToolListBuilder
	if cfg.ToolListBuilder != nil {
		toolListBuilder = cfg.ToolListBuilder
	}

	// A nil slice means unrestricted; a non-nil one (even empty) is a whitelist.
	var collaborators map[string]bool
	if cfg.Collaborators != nil {
		collaborators = make(map[string]bool, len(cfg.Collaborators))
		for _, id := range cfg.Collaborators {
			collaborators[id] = true
		}
	}

	w := &BaseReasonWorker{
		BaseWorker:      baseworker.NewBaseWorker(cfg.ID, cfg.Bus),
		llmProvider:     initialProvider,
		providerSources: cfg.ProviderSources,
		providerName:    cfg.ProviderName,
		providerModel:   cfg.ProviderModel,
		transcript:      cfg.Transcript,
		eventConverters: cfg.EventConverters,
		programs:        programState{Applied: cfg.Programs},
		requestTracker:  requesttracker.NewRequestTracker(),
		toolListBuilder: toolListBuilder,
		budgetSoft:      cfg.BudgetSoft,
		budgetHard:      cfg.BudgetHard,
		tailTokens:      cfg.TailTokens,
		reasoningEffort: cfg.ReasoningEffort,
		readme:          cfg.Readme,
		workerType:      workerTypeOrDefault(cfg.WorkerType),
		collaborators:   collaborators,
		excludedPeers:   make(map[string]bool),
	}
	// Durable-change signalling is the base's machinery (baseworker); the
	// mechanism only decides when to raise it (see handleSetLLMProvider).
	w.SetOnDurableChange(cfg.OnDurableChange)
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
	// manual fallback (0 = disabled), and the model's output cap so requests
	// are not silently held to an adapter default.
	w.resolveContextWindow()
	w.resolveMaxOutput()

	return w
}

// ── Mechanism accessors for embedding workers ───────────────────────────────
//
// These expose the mechanism's operations to reason-family workers so their
// extension handlers can be implemented outside pkg/reason without reaching
// into unexported state. The default worker's toolkit (send_message, request,
// context.compress) lives in pkg/worker/reason and is built on exactly
// these.

// LLMProvider returns the currently active LLM provider. A worker's context
// strategy reads it lazily each call (e.g. for summarization) so it follows
// provider switches without rebinding.
func (w *BaseReasonWorker) LLMProvider() llm.LLMProvider { return w.llmProvider }

// CurrentTraceID returns the trace id of the event currently being processed.
func (w *BaseReasonWorker) CurrentTraceID() string { return w.currentTraceID }

// Transcript returns the worker's transcript for in-place editing. The context
// strategy (e.g. the default worker's context.compress handler) rewrites it to
// shrink the context; the caller already holds w.mu or edits inside a
// BeginEdit..Commit window.
func (w *BaseReasonWorker) Transcript() transcript.Transcript { return w.transcript }

// CommitTranscript ends a transcript edit (BeginEdit..Commit) and, when the edit
// installed a new head, records the prompt-cache reset it pays for: the front of
// the transcript is gone, so the round that follows is also where the program
// ledger reaches the system prompt. The commit and the arming share one critical
// section, so no request can be built between the new transcript and the prompt
// that must accompany it.
//
// The boundary is read from the edit, not declared by the caller: a head IS the
// new front (the digest that replaced older history), while a commit carrying no
// head only ends the edit — it merges inputs buffered during it and leaves the
// transcript an extension of the previous one, whose prefix cache is still warm.
// A no-op compression must therefore still commit, just with no head. A strategy
// that rewrites the front some other way owes the arming itself, via
// ApplyProgramsAtNextRound.
func (w *BaseReasonWorker) CommitTranscript(head, tail []llm.Message) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.transcript.Commit(head, tail) && len(head) > 0 {
		w.programs.ApplyPending = true
	}
}

// TailTokens returns the token budget the default context strategy retains from
// the tail of the transcript when compressing (the worker's own policy; the
// mechanism does not use it).
func (w *BaseReasonWorker) TailTokens() int { return w.tailTokens }

// TryReason asks the mechanism to schedule the next reasoning round after the
// worker has finished a context edit. The worker drives this (it knows when
// its async edit completed); the mechanism only owns the scheduling.
func (w *BaseReasonWorker) TryReason(ctx context.Context) {
	w.mu.Lock()
	w.needReason = true
	w.mu.Unlock()
	w.tryReason(ctx)
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
	// The durable-change signal (baseworker) delivers the persistence
	// callback off the event loop; no goroutine when nothing is installed.
	w.StartDurableLoop(runCtx)

	// Disclose both directions on startup: BroadcastReady reaches whoever is
	// already up, BroadcastDiscover makes those workers reply DIRECTLY (see
	// process.go), so this worker also learns those that predate it.
	w.BroadcastReady()
	w.BroadcastDiscover()

	w.started = true
	return nil
}

// watch is the single event loop goroutine. It blocks on busCh waiting
// for events, calls process() to handle them, and then calls tryReason()
// which starts reasoning when needReason is set and no reasoning is running.
func (w *BaseReasonWorker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt, ok := <-busCh:
			if !ok {
				return
			}
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
// The transcript and the request tracker are carried as raw blobs because their
// shape belongs to the transcript / the tracker, not to us. Provider is the
// provider/model the worker is currently using, so a worker switched at runtime
// (provider.switch) resumes on that choice instead of silently
// falling back to the configured default — without it, a switch would be lost
// on every restart.
type reasonState struct {
	Transcript json.RawMessage    `json:"transcript"`
	Provider   *providerSelection `json:"provider,omitempty"`
	// ProgramState is the worker's program attribute: the applied set (what the
	// system prompt renders) plus the ledger of operations not applied yet. Both
	// halves travel together so a restart owes exactly the same apply.
	ProgramState *programState `json:"program_state,omitempty"`
	// Programs is the pre-ledger form of the same attribute: a bare list that was
	// the applied set outright. Read-only compatibility — snapshots taken before
	// the ledger existed carry it instead of ProgramState.
	Programs []program.Program `json:"programs,omitempty"`
	// Readme is the worker's self-description. A pointer distinguishes "never
	// set at runtime" (old snapshots, keep the config seed) from "cleared by the
	// worker" (empty string, override the seed).
	Readme *string `json:"readme,omitempty"`
	// Requests is the tracker's blob: the tool calls still in flight when the
	// snapshot was taken. Both halves of one wait (the transcript's placeholder
	// and the tracker's entry) are captured together, so a [pending] message is
	// never restored without its request.
	Requests json.RawMessage `json:"requests,omitempty"`
	// ToolCallSeq is the id counter behind synthesized tool-call ids. Its
	// uniqueness domain is the whole transcript (every id it has ever seen),
	// not just the calls currently tracked: resetting it on restart would
	// re-mint an id still present in the restored history, and a result would
	// then patch that old message instead of the new call's placeholder.
	ToolCallSeq uint64 `json:"tool_call_seq"`
	// ContextUsage is the last completed round's context accounting. It rides in
	// the snapshot because the provider's numbers cannot be re-derived from the
	// restored transcript — without it, context.status would report nothing
	// until the worker's next round.
	ContextUsage *contextUsage `json:"context_usage,omitempty"`
}

type providerSelection struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// Snapshot captures the worker's durable execution state: the transcript, the
// tool calls it is still awaiting, and its current provider/model selection.
func (w *BaseReasonWorker) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	transcript, err := w.transcript.State()
	if err != nil {
		return nil, err
	}
	state := reasonState{Transcript: transcript}
	// The tracker rides along in whatever snapshot is taken (suspend,
	// checkpoint, shutdown) — it raises no durable-change signal of its own,
	// because a pending call only means something together with the transcript
	// placeholder it belongs to.
	requests, err := w.requestTracker.State()
	if err != nil {
		return nil, err
	}
	state.Requests = requests
	state.ToolCallSeq = w.toolCallSeq
	state.ContextUsage = w.contextUsage
	// Only persist a selection the worker actually made; a worker left on its
	// configured default stays on it (and picks up config changes to it).
	if w.providerName != "" && w.providerModel != "" {
		state.Provider = &providerSelection{Name: w.providerName, Model: w.providerModel}
	}
	// Programs: a single attribute (config seeds it; runtime edits mutate it).
	// Always persist the current value so restore unconditionally overrides
	// config — no "edited" flag, unlike Provider. The applied set and its pending
	// ledger travel together, so a restart owes exactly the same apply.
	state.ProgramState = w.programs.copy()
	// Readme: always persist the current value, including empty (the worker may
	// have deliberately cleared its description).
	readme := w.readme
	state.Readme = &readme
	return json.Marshal(state)
}

// Restore rehydrates the worker from a Snapshot blob, restoring the transcript,
// the tool calls it was awaiting, and the persisted provider/model selection.
//
// A restored wait is always parked: the invocation was issued by a worker
// instance that no longer exists, and in practice its target does not have it
// either (a restarted peer has forgotten it, and nothing reaps a pending call
// on worker.gone). Waiting on it would be a wait that can never end — and one
// that now survives restarts, since Resolved() gates schedule-mode input.
// Parking keeps the tracker and the transcript in step: the placeholder is
// rewritten with the explanation, and a result that still arrives is matched as
// a late result instead of being dropped.
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
	w.toolCallSeq = s.ToolCallSeq
	w.contextUsage = s.ContextUsage
	// The tracker is bookkeeping, not memory: a corrupt blob must not keep the
	// daemon from starting (Restore's error fails project recovery), and losing
	// it only means late results go unmatched. The transcript is the opposite:
	// its failure is still fatal above.
	if err := w.requestTracker.Restore(s.Requests); err != nil {
		log.Printf("[reason %s] restore: %v; tool results for those calls will not be matched", w.ID(), err)
	}
	// Park after the transcript is restored — the patches rewrite placeholders
	// that Restore just brought back. No request.cancel here (unlike
	// parkPending): nothing has been sent on this instance's channel yet.
	w.parkRequests(requesttracker.PreemptCauseRestart)
	// Programs override config whenever the snapshot actually carries them.
	// ProgramState is the current form (applied set + pending ledger); a
	// ProgramState-less snapshot with Programs present predates the ledger, and
	// its bare list is the applied set. Field-absent snapshots (taken before
	// Programs existed) leave config standing — a backward-compat guard, not a
	// "managed" flag.
	switch {
	case s.ProgramState != nil:
		w.programs = *s.ProgramState
	case s.Programs != nil:
		w.programs = programState{Applied: s.Programs}
	}
	// A restored transcript means a prompt may already be cached at the provider,
	// so a program change recorded here must wait for a reset boundary rather than
	// be rendered on the first round after the restart.
	w.promptSent = len(w.transcript.Render()) > 0
	// A readme updated by the worker itself wins over the config seed. Absent
	// (older snapshots) keeps the seed.
	if s.Readme != nil {
		w.readme = *s.Readme
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
// notes, the LLM call, budget and the run flags of one reasoning
// round. It knows how to reason, not what to attend to.
type BaseReasonWorker struct {
	baseworker.BaseWorker
	mu sync.Mutex

	llmProvider     llm.LLMProvider
	providerSources ProviderSources
	providerName    string // active provider name (status reporting)
	providerModel   string // active provider model (status reporting)
	transcript      transcript.Transcript
	discovered      []DiscoveredCapability // unified capability universe (bus announcements only; the self-ready round-trip includes this worker's own capabilities)
	toolListBuilder ToolListBuilder        // LLM tool list policy (extension point)

	// Collaboration scope. collaborators is nil when unrestricted; excludedPeers
	// accumulates reason peers that announced outside it, so a later directed
	// send can be refused. scopeMu guards both and is separate from mu because
	// MayAddress is called from paths that do not hold the event-loop lock.
	scopeMu       sync.RWMutex
	collaborators map[string]bool
	excludedPeers map[string]bool
	workerType    string // announced presence type (default "reason")
	// programs is the worker's program attribute: the applied set (what the
	// system prompt renders) plus the ledger of operations not applied yet.
	// See programs.go for why the two are kept apart.
	programs programState
	// promptSent records that a request carrying this worker's system prompt has
	// already gone out, so a program change must wait for a cache reset boundary
	// instead of being rendered immediately. Runtime only: a restored transcript
	// implies a sent prompt (see Restore).
	promptSent     bool
	readme         string // the worker's own description (worker.readme)
	requestTracker *requesttracker.RequestTracker
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
	activeTimeout           string                      // current round's timeout call_id, "" if none
	activeTimeoutProvider   string                      // worker that set the active timeout (set time), "" if none
	interruptReason         requesttracker.PreemptCause // why the current reasoning round was interrupted
	immediateReasoningCause requesttracker.PreemptCause // why the next reasoning round was triggered
	currentTraceID          string

	// Rate-limit backoff state (429 handling); guarded by w.mu
	rateLimitAttempts int // consecutive 429s consumed in the current backoff chain

	cancelReason context.CancelFunc
	cancelRun    context.CancelFunc

	// Context budget state; guarded by w.mu
	// contextWindow is the effective window of the currently selected model, always
	// derived (constructor and every provider switch call resolveContextWindow).
	// 0 means the model reports no window: budget handling stays disabled rather
	// than guessing a size — declaring one is provider.json's job, not the worker's.
	contextWindow int
	// maxOutput is the selected model's declared cap on generated tokens
	// (0 = unknown). Applied as Context.MaxTokens, so a model that reports a
	// large cap is no longer silently held to an adapter default.
	maxOutput       int
	budgetSoft      float64
	budgetHard      float64
	tailTokens      int
	lastUsageTokens int
	budgetReminded  bool

	// roundBreakdown is the current round's prompt composition, estimated in
	// prepareReasoning from the exact request that was sent and carried on the
	// round's reason.response (see contextbreakdown.go). Guarded by w.mu.
	roundBreakdown []contextSection
	// contextUsage is the last completed round's accounting, carried into the
	// snapshot so the state survives a restart (see contextstatus.go). Guarded
	// by w.mu.
	contextUsage *contextUsage
}

// workerTypeOrDefault returns the worker's announced type, defaulting to the
// generic reason worker's label.
func workerTypeOrDefault(t string) string {
	if t == "" {
		return "reason"
	}
	return t
}
