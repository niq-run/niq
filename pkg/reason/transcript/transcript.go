// Transcript and the sealed TranscriptPatch algebra: the context-construction
// contract a reasoning worker folds into. The godoc for the package lives in
// worker.go; this file is the transcript's interface plus the inputs that fold
// into it, and the tool-pairing invariant that constrains them.
package transcript

import "github.com/niq-run/niq/core/llm"

// Transcript is the working "notes" of a reasoning worker: a self-synchronized
// data structure that folds lifecycle facts (TranscriptPatch) into a working
// transcript and renders it into LLM messages per reasoning round.
//
// It is concurrency-safe by itself: an edit that must rewrite the transcript
// over a long, off-transcript computation (an LLM summary) can BeginEdit
// (snapshot), compute freely without holding any lock, then CommitEdit to
// apply. While an edit is in progress, Apply calls are buffered and merged on
// commit, so they are neither lost nor torn by the edit's overwrite.
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
//     messages only append, and digest changes happen only via CommitEdit.
//
// The interface is evolving: a single implementation (Accumulate) for now.
type Transcript interface {
	// Apply folds one lifecycle fact into the transcript. If an edit is in
	// progress (BeginEdit..CommitEdit), the input is buffered and merged on
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

	// CommitEdit ends an edit: applies the computed digest (replacing all
	// but the last keepTail messages, alignment-corrected) and merges any
	// Apply inputs buffered during the edit. No-op if no edit is in progress.
	CommitEdit(digest string, keepTail int)

	// AbortEdit cancels an edit without applying: clears the editing state
	// and drops nothing (buffered Apply inputs are kept in the main transcript;
	// if the edit was going to overwrite them, aborting returns it to normal
	// append-only). No-op if no edit is in progress.
	AbortEdit()

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

// ToolResultPatch replaces the placeholder for a resolved tool call.
type ToolResultPatch struct {
	CallID string
	Name   string
	Text   string
	IsErr  bool
}

// ToolParkedPatch replaces the placeholder for a parked (no longer awaited) call.
type ToolParkedPatch struct {
	CallID string
	Name   string
	Cause  string // why the call was parked (timeout / input / abort / reminder)
}

// LateResultPatch appends a contextualized user message for a late-arriving
// result on a parked call (a second tool_result for the same call_id would
// violate the pairing invariant).
type LateResultPatch struct {
	CallID string
	Name   string
	Text   string
	Cause  string
}

func (InputPatch) isTranscriptPatch()            {}
func (AssistantOutputPatch) isTranscriptPatch()  {}
func (PartialOutputPatch) isTranscriptPatch()    {}
func (ToolPlaceholdersPatch) isTranscriptPatch() {}
func (ToolResultPatch) isTranscriptPatch()       {}
func (ToolParkedPatch) isTranscriptPatch()       {}
func (LateResultPatch) isTranscriptPatch()       {}
