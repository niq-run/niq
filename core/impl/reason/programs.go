// Runtime program management: the worker's program attribute is a single
// programState seeded from config and mutated by the program.query /
// program.attach / program.detach meta-extensions.
//
// The attribute is split in two on purpose. The system prompt is the head of
// every request, so changing it invalidates the provider's entire cached
// prefix (every message behind it is re-read at full price). Therefore:
//
//	Applied — the set in force. This, and only this, renders into the system
//	          prompt (buildInstruction).
//	Delta   — the ledger of program operations issued since the last apply, in
//	          issue order. Replayed onto Applied by Apply.
//
// A program operation takes effect in the conversation immediately (its handler
// appends a [system] note describing it), but reaches the system prompt only at
// a moment whose cache cost is already being paid — see prepareReasoning and
// ApplyProgramsAtNextRound. The delta is persisted with the worker, so the
// account survives a restart still owing the same apply.
//
// Locking contract: extension handlers are invoked from process(), which holds
// w.mu for the whole dispatch. The *Locked variants below are for handlers —
// they must be called with w.mu already held and never take it themselves
// (sync.Mutex is not reentrant: locking inside a handler self-deadlocks the
// event loop). The plain-named variants take w.mu and are for callers outside
// the event loop.
package reason

import (
	"fmt"

	"github.com/niq-run/niq/core/itfs/program"
)

// ProgramOpKind is the operation a ProgramOp records.
type ProgramOpKind string

const (
	ProgramOpAttach ProgramOpKind = "attach"
	ProgramOpDetach ProgramOpKind = "detach"
)

// ProgramOp is one entry in the program ledger: an operation recorded but not
// yet folded into the applied set. It is persisted with the worker (see
// reasonState.ProgramState) and it is also the single source the attach/detach
// handlers render their [system] note from — the ledger and the transcript
// cannot drift apart.
//
// Program carries the whole entry for an attach (an instruction's text lives on
// this worker, not in the program worker) and is nil for a detach. ContentType
// is recorded at op time because the note's wording depends on it, and because
// the entry may already be gone from the applied set by the time it is applied.
type ProgramOp struct {
	Kind        ProgramOpKind       `json:"op"`
	Program     *program.Program    `json:"program,omitempty"`
	Name        string              `json:"name"`
	ContentType program.ContentType `json:"content_type,omitempty"`
}

// programState is the worker's program attribute, persisted as one piece.
type programState struct {
	Applied []program.Program `json:"applied"`
	Delta   []ProgramOp       `json:"delta,omitempty"`
	// ApplyPending records that a prompt-cache reset boundary (a compaction) has
	// already been paid for, so the next round folds the delta instead of waiting
	// for another boundary. It is persisted because the boundary and the fold can
	// straddle a checkpoint: the stored transcript may already be the
	// post-compaction one, in which case the program text it carried has been
	// summarized away and only the prompt can still carry it.
	ApplyPending bool `json:"apply_pending,omitempty"`
}

// copy returns a deep-enough copy for a snapshot. Under w.mu the slices are
// stable, but a snapshot must not alias the live state it may be marshaled
// alongside.
func (s *programState) copy() *programState {
	cp := &programState{ApplyPending: s.ApplyPending}
	cp.Applied = make([]program.Program, len(s.Applied))
	copy(cp.Applied, s.Applied)
	cp.Delta = make([]ProgramOp, len(s.Delta))
	copy(cp.Delta, s.Delta)
	return cp
}

// Effective returns Applied with the ledger folded in, without mutating the
// state: the list this worker is actually holding. It is what program.query
// reports and what name/lock checks run against, so a program whose attach is
// still pending counts as present (attach stays purely additive).
func (s *programState) Effective() []program.Program {
	out := make([]program.Program, len(s.Applied))
	copy(out, s.Applied)
	for _, op := range s.Delta {
		out = op.applyTo(out)
	}
	return out
}

// applyTo replays one recorded operation onto a program list.
func (op ProgramOp) applyTo(list []program.Program) []program.Program {
	switch op.Kind {
	case ProgramOpAttach:
		if op.Program == nil {
			return list
		}
		for i := range list {
			if list[i].Name == op.Program.Name {
				list[i] = *op.Program
				return list
			}
		}
		return append(list, *op.Program)
	case ProgramOpDetach:
		for i := range list {
			if list[i].Name == op.Name {
				return append(list[:i], list[i+1:]...)
			}
		}
	}
	return list
}

// find returns the effective entry with the given name.
func (s *programState) find(name string) (program.Program, bool) {
	for _, p := range s.Effective() {
		if p.Name == name {
			return p, true
		}
	}
	return program.Program{}, false
}

// Attach records an attach in the ledger. It errors if a program with the same
// name is already held — attach is purely additive; detach and attach again to
// replace. Locked programs may be attached (they are operator-set).
func (s *programState) Attach(p program.Program) (ProgramOp, error) {
	if _, exists := s.find(p.Name); exists {
		return ProgramOp{}, fmt.Errorf("program %q already exists", p.Name)
	}
	entry := p
	op := ProgramOp{Kind: ProgramOpAttach, Program: &entry, Name: p.Name, ContentType: p.ContentType}
	s.Delta = append(s.Delta, op)
	return op, nil
}

// Detach records a detach in the ledger. It errors if the program is absent or
// marked Locked (immutable system-level entries cannot be removed via the
// meta-extension, per program.Meta). The check runs against the effective set,
// so a locked program attached but not yet applied is protected too.
func (s *programState) Detach(name string) (ProgramOp, error) {
	p, ok := s.find(name)
	if !ok {
		return ProgramOp{}, fmt.Errorf("program %q not found", name)
	}
	if p.Locked {
		return ProgramOp{}, fmt.Errorf("program %q is locked and cannot be removed", name)
	}
	op := ProgramOp{Kind: ProgramOpDetach, Name: name, ContentType: p.ContentType}
	s.Delta = append(s.Delta, op)
	return op, nil
}

// Apply folds the ledger into Applied, in issue order, and clears it. It returns
// the operations that were settled (nil when nothing was pending). Applying an
// empty ledger still clears ApplyPending: the boundary has been consumed either
// way.
func (s *programState) Apply() []ProgramOp {
	ops := s.Delta
	if len(ops) > 0 {
		s.Applied = s.Effective()
	}
	s.Delta = nil
	s.ApplyPending = false
	return ops
}

// AttachProgram records an attach on this worker's ledger, taking w.mu itself.
// For a caller outside the event loop; handlers use AttachProgramLocked.
//
// It records the operation only. Once a prompt has gone out, a change is in force
// in the conversation solely through the [system] note its handler appends
// (workers/reason's program toolkit), so a programmatic attach after the first
// round leaves the model unaware until the next compaction — the caller owns
// that gap.
func (w *BaseReasonWorker) AttachProgram(p program.Program) (ProgramOp, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.programs.Attach(p)
}

// AttachProgramLocked is AttachProgram without taking w.mu. Expects w.mu held.
func (w *BaseReasonWorker) AttachProgramLocked(p program.Program) (ProgramOp, error) {
	return w.programs.Attach(p)
}

// DetachProgram records a detach on this worker's ledger, taking w.mu itself.
// For a caller outside the event loop; handlers use DetachProgramLocked. It
// records the operation only — see AttachProgram on the note a caller owes.
func (w *BaseReasonWorker) DetachProgram(name string) (ProgramOp, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.programs.Detach(name)
}

// DetachProgramLocked is DetachProgram without taking w.mu. Expects w.mu held.
func (w *BaseReasonWorker) DetachProgramLocked(name string) (ProgramOp, error) {
	return w.programs.Detach(name)
}

// Programs returns a copy of the currently held programs (applied set plus
// pending ledger), taking w.mu itself. For a caller outside the event loop;
// handlers use ProgramsLocked.
func (w *BaseReasonWorker) Programs() []program.Program {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ProgramsLocked()
}

// ProgramsLocked is Programs without taking w.mu. Expects w.mu held.
func (w *BaseReasonWorker) ProgramsLocked() []program.Program {
	return w.programs.Effective()
}

// PromptSentLocked reports whether a request carrying this worker's system
// prompt has already gone out, i.e. whether changing it would invalidate a
// provider-side prefix cache. Expects w.mu held.
func (w *BaseReasonWorker) PromptSentLocked() bool { return w.promptSent }

// ApplyProgramsAtNextRound records that a prompt-cache reset boundary has been
// paid for, so the next reasoning round folds the ledger into the system prompt
// without extra cost. A compaction does not call this — CommitTranscript derives
// the boundary from the edit itself. This is the hook for a future boundary that
// rewrites nothing the mechanism can see (the episode operations in
// doc/design/reason_worker/context-builder.md, say). A no-op edit must call
// neither: it changes no bytes, so the current prompt should stand.
func (w *BaseReasonWorker) ApplyProgramsAtNextRound() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.programs.ApplyPending = true
}

// applyProgramsLocked folds the ledger into the applied set now, for a caller
// that knows the cache cost is already being paid (a provider switch starts on
// a cold cache). Expects w.mu held. Returns the settled operations.
func (w *BaseReasonWorker) applyProgramsLocked() []ProgramOp {
	return w.programs.Apply()
}
