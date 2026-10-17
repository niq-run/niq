// Package transcript is the context-construction core of a reasoning worker:
// the Transcript interface and the sealed TranscriptPatch algebra (the inputs
// that fold into it), plus the default AccumulateTranscript implementation in
// transcript_accumulate.go. This file is the interface plus the patch variants
// and the tool-pairing invariant that constrains them.
package transcript

import "github.com/niq-run/niq/core/itfs/llm"

// Transcript is the working "notes" of a reasoning worker: a self-synchronized
// data structure that folds lifecycle facts (TranscriptPatch) into a working
// transcript and renders it into LLM messages per reasoning round.
//
// It is concurrency-safe by itself: an edit that must rewrite the transcript
// over a long, off-transcript computation can BeginEdit (snapshot), compute
// freely without holding any lock, then Commit to apply. While an edit is in
// progress, Apply calls are buffered and merged on commit, so they are neither
// lost nor torn by the edit's overwrite.
//
// Design invariants:
//   - Each method acquires and releases its lock internally; methods never
//     call back into the worker (so an external caller can never hold the
//     transcript lock while acquiring another lock — the lock lifetime is
//     bounded by a single method call).
//   - The transcript is a projection of the fact layer (the event store), not
//     the worker's identity. Operations transform the projection; facts remain.
//   - Inputs are sealed, data-only variants: the worker translates its
//     lifecycle into these variants.
//   - Render is an identity projection of the state: the system prompt comes
//     from the worker's programs (not here); the transcript renders only
//     messages, verbatim. Prefix stability (prompt cache) holds because
//     messages only append, and rewrites happen only via Commit.
//
// The interface is evolving: a single implementation (Accumulate) for now.
type Transcript interface {
	// Apply folds one lifecycle fact into the transcript. If an edit is in
	// progress (BeginEdit..Commit), the input is buffered and merged on
	// commit, not lost.
	Apply(input TranscriptPatch)

	// Render returns the message list for the next LLM round. The returned
	// slice must be treated as read-only.
	Render() []llm.Message

	// BeginEdit starts an edit: marks the transcript as being edited and returns
	// a snapshot. The lock is released before returning, so the caller
	// may compute on the snapshot (e.g. an LLM summary) without holding any
	// lock; Apply calls during this window are buffered.
	BeginEdit() []llm.Message

	// Commit ends an edit: rewrites the transcript to the caller-supplied head
	// messages followed by the caller-selected retained tail, then merges any
	// Apply inputs buffered during the edit and strips dangling tool calls.
	// head is the caller's new leading content (e.g. a digest message it built);
	// tail is the retained recent history it chose (see TailTokensBudget /
	// TailRoundsClean for the pairing-safe selectors). How to trim, whether to
	// fold, and what head to prepend are the caller's policy — this only
	// assembles head + tail and finalizes the editing state. It returns true if
	// an edit was in progress (the rewrite was applied); false if no edit was in
	// progress (no-op).
	Commit(head []llm.Message, tail []llm.Message) bool

	// AbortEdit cancels an edit without applying: clears the editing state
	// and drops nothing (buffered Apply inputs are kept in the main transcript;
	// if the edit was going to overwrite them, aborting returns it to normal
	// append-only). No-op if no edit is in progress.
	AbortEdit()

	// EstimatedTokens returns a rough estimate of the current transcript's token
	// size (messages only, excluding the system prompt). It is a heuristic used
	// for budget sizing and reporting (e.g. to tell a caller how much headroom
	// remains before a compress would become a no-op), not an exact count.
	EstimatedTokens() int

	// State returns the serializable snapshot (a cache of the projection).
	State() ([]byte, error)

	// Restore rehydrates from a State blob.
	Restore(state []byte) error
}

// TranscriptPatch is a sealed lifecycle fact translated by the reason worker.
// Exactly one variant is carried per Apply call; Apply order is history.
type TranscriptPatch interface{ isTranscriptPatch() }

// InputPatch carries externally-sourced messages (user input, reminders,
// timeout notices, abort records) into the
type InputPatch struct {
	Messages []llm.Message
}

// AssistantOutputPatch records a completed reasoning round's final message.
type AssistantOutputPatch struct {
	Message llm.Message
}

// PartialOutputPatch records content preserved from an interrupted round.
type PartialOutputPatch struct {
	Message llm.Message
}

// ToolPlaceholdersPatch inserts [pending] tool_result entries for dispatched
// tool calls, preserving transcript ordering. Replaced in place later.
type ToolPlaceholdersPatch struct {
	Calls []llm.ContentBlock
}

// ToolResultPatch replaces the placeholder for a resolved tool call. Content
// carries the outcome's llm content blocks (which may be multi-modal, e.g. an
// image), plus the error flag.
type ToolResultPatch struct {
	CallID  string
	Name    string
	Content []llm.ContentBlock
	IsErr   bool
}

// ToolParkedPatch replaces the placeholder for a parked (no longer awaited) call.
type ToolParkedPatch struct {
	CallID string
	Name   string
	Cause  string // why the call was parked (timeout / input / abort / reminder)
}

// LateResultPatch appends a contextualized user message for a late-arriving
// result on a parked call (a second tool_result for the same call_id would
// violate the pairing invariant). Content carries the outcome's llm content
// blocks (which may be multi-modal).
type LateResultPatch struct {
	CallID  string
	Name    string
	Content []llm.ContentBlock
	Cause   string
}

func (InputPatch) isTranscriptPatch()            {}
func (AssistantOutputPatch) isTranscriptPatch()  {}
func (PartialOutputPatch) isTranscriptPatch()    {}
func (ToolPlaceholdersPatch) isTranscriptPatch() {}
func (ToolResultPatch) isTranscriptPatch()       {}
func (ToolParkedPatch) isTranscriptPatch()       {}
func (LateResultPatch) isTranscriptPatch()       {}
