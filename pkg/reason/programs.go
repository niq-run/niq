// Runtime program management: the reason worker's instruction/playbook list
// (w.programs) is a single attribute seeded from config and mutated in place
// by the program.query / program.update meta-extensions.
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

	"github.com/niq-run/niq/core/program"
)

// AddProgram appends a new program. It errors if a program with the same name
// already exists — program.update "add" is purely additive; use "remove" then
// "add" to replace. Locked programs may be added (they are operator-set).
func (w *BaseReasonWorker) AddProgram(p program.Program) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.AddProgramLocked(p)
}

// AddProgramLocked is AddProgram without taking w.mu. Expects w.mu held.
func (w *BaseReasonWorker) AddProgramLocked(p program.Program) error {
	for _, e := range w.programs {
		if e.Name == p.Name {
			return fmt.Errorf("program %q already exists", p.Name)
		}
	}
	w.programs = append(w.programs, p)
	return nil
}

// RemoveProgram deletes the named program. It errors if the program is absent
// or marked Locked (immutable system-level entries cannot be removed via the
// meta-extension, per program.Meta).
func (w *BaseReasonWorker) RemoveProgram(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.RemoveProgramLocked(name)
}

// RemoveProgramLocked is RemoveProgram without taking w.mu. Expects w.mu held.
func (w *BaseReasonWorker) RemoveProgramLocked(name string) error {
	for i, e := range w.programs {
		if e.Name == name {
			if e.Locked {
				return fmt.Errorf("program %q is locked and cannot be removed", name)
			}
			w.programs = append(w.programs[:i], w.programs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("program %q not found", name)
}

// ListPrograms returns a copy of the current programs for read-only callers
// outside the event loop.
func (w *BaseReasonWorker) ListPrograms() []program.Program {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ProgramsLocked()
}

// ProgramsLocked is ListPrograms without taking w.mu. Expects w.mu held.
func (w *BaseReasonWorker) ProgramsLocked() []program.Program {
	out := make([]program.Program, len(w.programs))
	copy(out, w.programs)
	return out
}
