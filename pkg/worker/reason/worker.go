package reason

import (
	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/core/program"
	reasonBase "github.com/54c1/niq/pkg/reason"
)

// Config holds the configuration for a generic "reason" worker.
type Config struct {
	ID              string
	EventConverters []reasonBase.EventConverter
	Provider        llm.LLMProvider
	ProviderSources reasonBase.ProviderSources
	// ProviderName / ProviderModel record the initially active provider for
	// status reporting (worker.status op=providers carries the current choice).
	// Empty when unknown.
	ProviderName    string
	ProviderModel   string
	Programs        []program.Program
	Bus             corebus.WorkerSideChannel
	ReasoningEffort *string

	// Transcript is the worker's context construction core. nil uses the
	// default accumulate implementation.
	Transcript reasonBase.Transcript

	// Context budget (see pkg/reason/compact.go). ContextWindow is the model's
	// window in tokens; 0 disables budget handling. BudgetSoft/BudgetHard are
	// occupancy ratios; KeepTail is how many recent messages compaction
	// preserves; CompactDirective overrides the fallback summarizer prompt.
	ContextWindow    int
	BudgetSoft       float64
	BudgetHard       float64
	KeepTail         int
	CompactDirective string

	// MaxPayloadBytes caps a single text payload folded into the transcript
	// (see reasonBase.Config). 0 uses the default.
	MaxPayloadBytes int

	// SeedMessages are applied to the transcript at construction: the
	// spawner's handover brief (goal goes to Programs instead). nil for a
	// fresh worker.
	SeedMessages []llm.Message
}

// Worker is the generic reason-family worker: it embeds the shared reasoning
// mechanism BaseReasonWorker (the reasoning round, budget, tool dispatch,
// system prompt, lifecycle) and is registered as a "reason" worker, layering
// on the built-in subscriptions and converters.
//
// Start/Stop/Snapshot/Restore and the whole reasoning behavior are inherited
// from BaseReasonWorker; this worker only assembles a Worker from Config.
type Worker struct {
	*reasonBase.BaseReasonWorker
}

// Re-exports for callers (swarm) that import the generic reason worker by
// its package name.
type EventConverter = reasonBase.EventConverter

// DefaultConverter formats an event as a plain-text user message.
var DefaultConverter = reasonBase.DefaultConverter

// NewWorker creates a generic reason Worker from the given configuration.
func NewWorker(cfg Config) *Worker {
	// Built-in subscriptions plus the custom converters' patterns.
	subs := make([]event.EventPattern, 0, len(cfg.EventConverters)+12)
	for _, h := range cfg.EventConverters {
		subs = append(subs, h.Pattern)
	}
	subs = append(subs,
		event.NewPattern(event.TypeToolCompleted),
		event.NewPattern(event.TypeToolFailed),
		event.NewPattern(event.TypeToolRejected),
		event.NewPattern(event.TypeToolRequest),
		event.NewPattern(event.TypeWorkerReady),
		event.NewPattern(event.TypeWorkerGone),
		event.NewPattern(event.TypeWorkerDiscover),
		event.NewPattern(event.TypeWorkerInput),
		event.NewPattern(event.TypeWorkerAbort),
		event.NewPattern(event.TypeWorkerUpdate),
		event.NewPattern(event.TypeWorkerQuery),
		event.NewPattern("timer.timeout"),
		event.NewPattern("timer.reminder"),
	)

	base := reasonBase.NewBaseReasonWorker(reasonBase.Config{
		ID:               cfg.ID,
		Bus:              cfg.Bus,
		Subscriptions:    subs,
		Provider:         cfg.Provider,
		ProviderSources:  cfg.ProviderSources,
		ProviderName:     cfg.ProviderName,
		ProviderModel:    cfg.ProviderModel,
		Programs:         cfg.Programs,
		EventConverters:  cfg.EventConverters,
		Transcript:       cfg.Transcript,
		ReasoningEffort:  cfg.ReasoningEffort,
		ContextWindow:    cfg.ContextWindow,
		BudgetSoft:       cfg.BudgetSoft,
		BudgetHard:       cfg.BudgetHard,
		KeepTail:         cfg.KeepTail,
		CompactDirective: cfg.CompactDirective,
		MaxPayloadBytes:  cfg.MaxPayloadBytes,
		SeedMessages:     cfg.SeedMessages,
	})

	return &Worker{BaseReasonWorker: base}
}
