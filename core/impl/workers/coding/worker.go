// Package coding provides the coding reason worker — a reason-family worker that
// runs PARALLEL to the generic reason worker (same shared mechanism
// core/impl/reason, its own composition), tuned for coding work.
//
// It differs from the generic reason worker in exactly two ways:
//
//   - the tool-list policy (ToolListBuilder) can run in "ptc" mode: the model
//     sees only the program worker's tools (plus this worker's own self tools)
//     and drives work through script programs (program.run), discovering other
//     workers' tools via the directory worker FROM INSIDE a script;
//   - compaction keeps a coding working set (the files already edited) in the
//     carried digest.
//
// Everything else — the reasoning round, budget, tool dispatch, system-prompt
// rendering, lifecycle — is inherited from reason.BaseReasonWorker.
package coding

import (
	"fmt"

	reasonBase "github.com/niq-run/niq/core/impl/reason"
	"github.com/niq-run/niq/core/impl/reason/transcript"
	reasonworkers "github.com/niq-run/niq/core/impl/workers/reason"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/llm"
	"github.com/niq-run/niq/core/itfs/program"
)

// Tool-list modes.
const (
	// ToolModeNormal exposes every discovered capability, like the generic
	// reason worker.
	ToolModeNormal = "normal"
	// ToolModePTC exposes only the program worker's tools (and this worker's own
	// self tools). Other workers' tools are discovered via the directory worker
	// from inside a script and executed by running that script (program.run).
	ToolModePTC = "ptc"
)

// DefaultProgramWorkerID is the program worker whose tools PTC mode keeps.
const DefaultProgramWorkerID = "program"

// DefaultDirectoryWorkerID is the directory worker whose tools PTC mode keeps —
// capability DISCOVERY is directory's job (program is capability INVOCATION), so
// it is exposed directly rather than being reached through a script.
const DefaultDirectoryWorkerID = "directory"

// Config holds the configuration for a coding reason worker.
type Config struct {
	ID              string
	EventConverters []reasonBase.EventConverter
	Provider        llm.LLMProvider
	ProviderSources reasonBase.ProviderSources
	ProviderName    string
	ProviderModel   string
	Programs        []program.Program
	Bus             corebus.WorkerSideChannel
	ReasoningEffort *string

	OnDurableChange func()

	Transcript transcript.Transcript

	ContextWindow    int
	BudgetSoft       float64
	BudgetHard       float64
	TailTokens       int
	RotateRounds     int
	CompactDirective string

	MaxPayloadBytes int
	SeedMessages    []llm.Message
	Readme          string

	// ToolMode selects the tool-list policy: ToolModeNormal (default) or
	// ToolModePTC.
	ToolMode string
	// ProgramWorkerID is the program worker whose tools ToolModePTC keeps.
	// Empty uses DefaultProgramWorkerID.
	ProgramWorkerID string
	// DirectoryWorkerID is the directory worker whose tools ToolModePTC keeps
	// (capability discovery). Empty uses DefaultDirectoryWorkerID.
	DirectoryWorkerID string
}

// Worker is the coding reason worker: it embeds the shared reasoning mechanism.
type Worker struct {
	*reasonBase.BaseReasonWorker
}

// NewWorker assembles a coding reason worker.
func NewWorker(cfg Config) *Worker {
	mode := cfg.ToolMode
	if mode == "" {
		mode = ToolModeNormal
	}
	programID := cfg.ProgramWorkerID
	if programID == "" {
		programID = DefaultProgramWorkerID
	}
	directoryID := cfg.DirectoryWorkerID
	if directoryID == "" {
		directoryID = DefaultDirectoryWorkerID
	}

	// Coding compaction directive: keep the working set in the carried digest.
	compactDirective := cfg.CompactDirective
	if compactDirective == "" {
		compactDirective = reasonworkers.FallbackCompactDirective + compactionAddendum
	}

	programs := cfg.Programs
	if mode == ToolModePTC {
		// Seed the PTC guidance as a locked instruction program so it renders in
		// the system prompt every round.
		programs = append([]program.Program{ptcInstructionProgram(programID)}, programs...)
	}

	base := reasonBase.NewBaseReasonWorker(reasonBase.Config{
		ID:              cfg.ID,
		Bus:             cfg.Bus,
		Provider:        cfg.Provider,
		ProviderSources: cfg.ProviderSources,
		ProviderName:    cfg.ProviderName,
		ProviderModel:   cfg.ProviderModel,
		Programs:        programs,
		EventConverters: cfg.EventConverters,
		Transcript:      cfg.Transcript,
		ReasoningEffort: cfg.ReasoningEffort,
		OnDurableChange: cfg.OnDurableChange,
		ContextWindow:   cfg.ContextWindow,
		BudgetSoft:      cfg.BudgetSoft,
		BudgetHard:      cfg.BudgetHard,
		TailTokens:      cfg.TailTokens,
		RotateRounds:    cfg.RotateRounds,
		MaxPayloadBytes: cfg.MaxPayloadBytes,
		SeedMessages:    cfg.SeedMessages,
		Readme:          cfg.Readme,
		ToolListBuilder: toolListBuilder(mode, programID, directoryID),
	})

	// Reuse the default reason-family toolkit (message / request /
	// context.compress / program.*) — the coding worker shares the baseline and
	// only differs in its tool-list policy and compaction.
	reasonworkers.RegisterDefaultExtensions(base, compactDirective)

	return &Worker{BaseReasonWorker: base}
}

// codingExcludedSelfEvents are the worker's OWN tools a coding agent should not
// spend rounds on: it compacts automatically under budget pressure (the
// mechanism fires context.compress itself; the model need not call it) and its
// programs are fixed at creation time, so it neither reads nor rewrites its own
// program list.
var codingExcludedSelfEvents = map[event.EventType]bool{
	reasonBase.TypeContextCompress:  true,
	reasonworkers.TypeProgramQuery:  true,
	reasonworkers.TypeProgramUpdate: true,
}

// toolListBuilder returns the LLM tool-list policy for the mode.
//
// Coding focus applies in BOTH modes: context.compress and program.update are
// always dropped from the model's tool list. In PTC mode the list additionally
// keeps this worker's own tools, the directory worker's (capability DISCOVERY)
// and the program worker's (capability INVOCATION), drops every other peer tool,
// and also drops the self `request` escape hatch so peer calls go through
// program.run rather than an ad-hoc event call.
func toolListBuilder(mode, programID, directoryID string) reasonBase.ToolListBuilder {
	ptc := mode == ToolModePTC
	return func(w *reasonBase.BaseReasonWorker, caps []reasonBase.DiscoveredCapability) []llm.ToolDef {
		keep := make([]reasonBase.DiscoveredCapability, 0, len(caps))
		for _, c := range caps {
			self := c.Source == w.ID()
			if self && codingExcludedSelfEvents[c.Event] {
				continue
			}
			if ptc {
				if self && c.Event == reasonBase.TypeRequest {
					continue
				}
				if !self && c.Source != programID && c.Source != directoryID {
					continue
				}
			}
			keep = append(keep, c)
		}
		return reasonBase.DefaultToolListBuilder(w, keep)
	}
}

// ptcInstructionProgram is the locked instruction seeded in PTC mode: it tells
// the model how to discover and execute tools while its LLM tool list holds only
// the program worker's tools.
func ptcInstructionProgram(programID string) program.Program {
	name := "coding-ptc-tooling"
	text := fmt.Sprintf(`## PTC tool mode
Your tool list holds the directory worker's tools (capability discovery), the %[1]s worker's tools (capability invocation), and your own message tool. Every other worker's tools are intentionally not listed — do not expect a flat tool list.

**Discover** what other workers offer with the directory worker, called directly as a tool:
- directory list_workers — the roster of workers;
- directory get_worker_info { worker: "<id>" } — one worker's capability events, arguments and schemas.
Discover once up front, then act; avoid re-listing tools you already looked up.

**Execute** by running a TypeScript script with the %[1]s worker's run tool. Two modes:
- source: pass the script inline for a one-off;
- name: store it first (write/upsert) and run it by name to reuse.

The script API (inside the script):
- await sdk.call(target, event, args) — invoke a capability as a normal request (returns its result);
- sdk.log(text); sdk.result(finalPayload); sdk.args (the run arguments).
The script body is async (top-level await works) and cannot import fs/net — it reaches the world only through sdk.call.

Note: there is no request tool in this mode — to invoke a peer capability, do it inside a script via sdk.call. Context compaction happens automatically under budget pressure; you do not need to call it.

Prefer one script that performs the whole sequence (act, then summarise) over many separate tool calls.`, programID)
	return program.Program{
		Meta: program.Meta{
			Name:        name,
			ContentType: program.ContentTypeInstruction,
			Description: "How to work in PTC tool mode: discover via the directory worker's tools, execute via the program worker's run.",
			Locked:      true,
		},
		EntryContent: program.ProgramContent{
			Name:     name,
			FormType: program.FormTypePrompt,
			Content:  text,
		},
	}
}

// compactionAddendum is appended to the summarizer directive so a compacted
// coding transcript keeps its working set.
const compactionAddendum = `

This is a CODING session. In the digest, always preserve:
- the exact list of files already created or edited (absolute paths), with a one-line note of what changed in each;
- the current task state and the next concrete step;
- any failed commands/tests and why, so they are not retried blindly.`
