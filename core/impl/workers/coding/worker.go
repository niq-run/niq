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
	// ToolModeNormal uses the reason worker's minimal surface unchanged:
	// message, request, and the directory worker's discovery events.
	ToolModeNormal = "normal"
	// ToolModePTC (Programmatic Tool Call) exposes only the program worker's
	// tools (and this worker's own self tools). Other workers' tools are
	// discovered via the directory worker from inside a script and executed by
	// running that script (program.run).
	ToolModePTC = "ptc"
)

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

	// Collaborators is the reason-worker collaboration whitelist: nil (default)
	// means every reason peer is visible, a non-nil slice is an allowlist (empty
	// = none). See reasonBase.Config.Collaborators.
	Collaborators []string

	// ToolMode selects the tool-list policy: ToolModeNormal (default) or
	// ToolModePTC.
	//
	// Which peers PTC keeps is NOT configurable: the program worker is the
	// invocation channel and the directory worker is discovery, both fixed by
	// the PTC contract (see reasonBase.ProgramWorkerType /
	// reasonBase.DirectoryWorkerType).
	ToolMode string
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
	// Coding compaction directive: keep the working set in the carried digest.
	compactDirective := cfg.CompactDirective
	if compactDirective == "" {
		compactDirective = reasonworkers.FallbackCompactDirective + compactionAddendum
	}

	programs := cfg.Programs
	if mode == ToolModePTC {
		// Seed the PTC guidance as a locked instruction program so it renders in
		// the system prompt every round.
		programs = append([]program.Program{ptcInstructionProgram()}, programs...)
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
		Collaborators:   cfg.Collaborators,
		ToolListBuilder: toolListBuilder(mode),
		// Announce as "coding" (not the default "reason") so peers, the
		// directory and the control plane see the accurate type.
		WorkerType: "coding",
	})

	// Reuse the default reason-family toolkit (message / request /
	// context.compress / program.*) — the coding worker shares the baseline and
	// only differs in its tool-list policy and compaction.
	reasonworkers.RegisterDefaultExtensions(base, compactDirective)

	return &Worker{BaseReasonWorker: base}
}

// ptcScope is PTC's visible range: message and run_program, nothing else.
//
// PTC is "scripts in place of direct sends", so request is left out: everything
// the model does goes through a script, including discovery (the directory is
// reached with sdk.call, not as a tool).
var ptcScope = reasonBase.ToolScope{
	Own: []event.EventType{reasonBase.TypeMessage, reasonBase.TypeRunProgram},
}

// toolListBuilder returns the LLM tool-list policy for the mode.
//
// Normal mode is the reason worker's minimal scope, unchanged — message, request
// and the directory worker's surface. Being a coding agent does not widen the
// tool list; the one coding-specific policy is PTC, which swaps
// discovery-by-request for scripted invocation by adding the program worker and
// dropping request (see ptcScope).
func toolListBuilder(mode string) reasonBase.ToolListBuilder {
	if mode != ToolModePTC {
		return reasonBase.ScopedToolListBuilder(reasonBase.DefaultToolScope)
	}
	return reasonBase.ScopedToolListBuilder(ptcScope)
}

// ptcInstructionProgram is the locked instruction seeded in PTC mode: it tells
// the model how to work when its tool list is only message and run_program.
func ptcInstructionProgram() program.Program {
	name := "coding-ptc-tooling"
	text := `## PTC tool mode
Your tool list holds just two tools: message, and run_program. There is no request tool and no directory tool — run_program is how you reach everything. You write a script; inside it, await sdk.call(target, event, arguments) sends exactly the event a request would, and program.run executes it under your identity as the caller's delegate.

**Discover** from inside the script:
- await sdk.call("directory", "search", {}) — the roster of workers;
- await sdk.call("directory", "search", { query: "..." }) — narrow by keyword;
- await sdk.call("directory", "describe", { worker: "<id>" }) — one worker's capability events, each with "parameters" (request schema), "response" (reply schema, when declared) and "desc".
Discover once up front, then act; avoid re-listing tools you already looked up.

**Execute** with run_program. Two modes:
- source: pass the script inline for a one-off;
- path: store it first (touch + write) and run it by path to reuse.

The script API (inside the script; the sandbox has no imports — no require/import/dynamic import() — and no filesystem, process or network access; only the JavaScript builtins plus sdk):
- await sdk.call(target, event, args) — invoke a capability as a normal request and get its reply VALUE: a string for prose, a real JS object/array for a structured reply (check the target's "response" via the directory worker's describe event, then read the fields directly — no JSON.parse). A tool-level failure comes back as a result, not an exception (bash exiting 3 resolves "Exit code: 3"); only an unknown worker / failed / rejected request throws. Fan out with Promise.all — calls are concurrent.
- await sdk.message(target, text) — the fire-and-forget sibling of call: sends a worker.input note (append mode) and resolves once it is on the bus. The recipient answers nothing; use it instead of sdk.call(target, "worker.input", {text}).
- sdk.result(payload) — string verbatim, other values JSON; last call wins. A top-level return of a payload does the same when sdk.result is never called. With neither, the result is "".
- sdk.log(text) — comes back to you after the result under "--- script log ---" (tail-capped); use it to trace a long run.
- sdk.args — the run args object (defaults to {}); read stored scripts' inputs from here.
The body is async (top-level await works). A script error is reported with the failing line of your own source, so a failing run is fixable in one round.

Note: there is no request tool in this mode — to invoke anything, do it inside a script via sdk.call. Context compaction happens automatically under budget pressure; you do not need to call it.

Prefer one script that performs the whole sequence (act, then summarise) over many separate tool calls.`
	return program.Program{
		Meta: program.Meta{
			Name:        name,
			ContentType: program.ContentTypeInstruction,
			Description: "How to work in PTC tool mode: message and run_program only; discover and act from inside the script.",
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
