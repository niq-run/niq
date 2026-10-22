package coding

import (
	"context"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// stubChannel is a minimal WorkerSideChannel so the worker can be constructed
// without a real bus.
type stubChannel struct{}

func (stubChannel) ID() string                                         { return "stub" }
func (stubChannel) Send(context.Context, event.Event, ...string) error { return nil }
func (stubChannel) Broadcast(context.Context, event.Event) error       { return nil }
func (stubChannel) Receive(context.Context) (<-chan event.Event, error) {
	ch := make(chan event.Event)
	close(ch)
	return ch, nil
}
func (stubChannel) Close() error { return nil }

// announce feeds a worker.ready watch as if a peer had announced it.
func announce(w *Worker, workerID, workerType string, events ...string) {
	watch := make([]map[string]any, 0, len(events))
	for _, e := range events {
		watch = append(watch, map[string]any{"event": e, "desc": e})
	}
	w.HandleWorkerReady(event.New(event.TypeWorkerReady, workerID, map[string]any{
		"worker_id": workerID,
		"type":      workerType,
		"watch":     watch,
	}))
}

func toolNames(w *Worker) map[string]bool {
	names := map[string]bool{}
	for _, d := range w.LLMToolDefs() {
		names[d.Name] = true
	}
	return names
}

func newCoding(t *testing.T, mode string) *Worker {
	t.Helper()
	w := NewWorker(Config{ID: "coder", Bus: stubChannel{}, ToolMode: mode})
	// Peers: the program worker and a workspace worker.
	announce(w, "program", "program", "search", "load", "run")
	announce(w, "workspace", "workspace", "bash")
	announce(w, "directory", "directory", "list_workers", "get_worker_info")
	return w
}

// TestPTCModeExposesDirectoryAndProgramTools verifies PTC mode keeps the
// directory worker's tools (discovery) and the program worker's tools
// (invocation) plus the worker's own self tools — every other peer tool is
// dropped from the LLM tool list.
func TestPTCModeExposesDirectoryAndProgramTools(t *testing.T) {
	w := newCoding(t, ToolModePTC)
	names := toolNames(w)

	for _, want := range []string{
		"program__run", "program__search", "program__load",
		"directory__list_workers", "directory__get_worker_info",
	} {
		if !names[want] {
			t.Errorf("PTC tool list missing %q (got %v)", want, names)
		}
	}
	if names["workspace__bash"] {
		t.Error("PTC tool list must not include workspace__bash (reached through a script)")
	}
}

// TestPTCModeKeepsMessageButDropsRequestAndFocusTools verifies PTC keeps the
// agent-to-agent message channel but drops the request escape hatch (peer calls
// must go through program.run) and the focus-excluded self tools.
func TestPTCModeKeepsMessageButDropsRequestAndFocusTools(t *testing.T) {
	w := NewWorker(Config{ID: "coder", Bus: stubChannel{}, ToolMode: ToolModePTC})
	// The worker's own full contract (Internal included), as it sends itself.
	announce(w, "coder", "reason", "message", "request", "context.compress", "program.query", "program.update")
	announce(w, "program", "program", "run")
	announce(w, "directory", "directory", "list_workers", "get_worker_info")
	announce(w, "workspace", "workspace", "bash")

	names := toolNames(w)
	for _, want := range []string{"message", "program__run", "directory__list_workers"} {
		if !names[want] {
			t.Errorf("PTC should keep %q (got %v)", want, names)
		}
	}
	for _, unwanted := range []string{"request", "context_compress", "program_query", "program_update", "workspace__bash"} {
		if names[unwanted] {
			t.Errorf("PTC must drop %q (got %v)", unwanted, names)
		}
	}
}

// TestNormalModeDropsFocusToolsButKeepsRequest verifies the coding focus filter
// applies in normal mode too: context.compress and both program tools are
// dropped (programs are fixed at creation), while request and every peer tool
// stay.
func TestNormalModeDropsFocusToolsButKeepsRequest(t *testing.T) {
	w := NewWorker(Config{ID: "coder", Bus: stubChannel{}, ToolMode: ToolModeNormal})
	announce(w, "coder", "reason", "message", "request", "context.compress", "program.query", "program.update")
	announce(w, "workspace", "workspace", "bash")

	names := toolNames(w)
	for _, want := range []string{"message", "request", "workspace__bash"} {
		if !names[want] {
			t.Errorf("normal mode should keep %q (got %v)", want, names)
		}
	}
	for _, unwanted := range []string{"context_compress", "program_query", "program_update"} {
		if names[unwanted] {
			t.Errorf("normal mode must drop %q (got %v)", unwanted, names)
		}
	}
}

// TestProviderToolsRegisteredButHidden verifies the model-management tools are
// still registered (peers / the control plane can switch this worker's model)
// while being hidden from the agent's own LLM tool list.
func TestProviderToolsRegisteredButHidden(t *testing.T) {
	w := NewWorker(Config{ID: "coder", Bus: stubChannel{}})
	announce(w, "coder", "reason", "message", "provider.switch", "provider.list", "provider.current")

	// Registered: peers can still call them.
	have := map[string]bool{}
	for _, e := range w.Extensions() {
		have[string(e.Event)] = true
	}
	for _, want := range []string{"provider.switch", "provider.list", "provider.current"} {
		if !have[want] {
			t.Errorf("provider tool %q must stay registered (got %v)", want, have)
		}
	}

	// Hidden from the model.
	names := toolNames(w)
	for _, unwanted := range []string{"provider_switch", "provider_list", "provider_current"} {
		if names[unwanted] {
			t.Errorf("provider tool %q must not be in the LLM tool list (got %v)", unwanted, names)
		}
	}
	if !names["message"] {
		t.Error("message should stay in the LLM tool list")
	}
}

// TestNormalModeExposesAllTools verifies normal mode keeps every discovered
// capability, like the generic reason worker.
func TestNormalModeExposesAllTools(t *testing.T) {
	w := newCoding(t, ToolModeNormal)
	names := toolNames(w)
	for _, want := range []string{"program__run", "workspace__bash", "directory__list_workers"} {
		if !names[want] {
			t.Errorf("normal tool list missing %q (got %v)", want, names)
		}
	}
}

// TestPTCSeedsInstructionProgram verifies PTC mode seeds the locked tooling
// instruction that explains discovery via directory + execution via program.run.
func TestPTCSeedsInstructionProgram(t *testing.T) {
	w := newCoding(t, ToolModePTC)
	found := false
	for _, p := range w.ProgramsLocked() {
		if p.Name == "coding-ptc-tooling" {
			found = true
			if !p.Locked {
				t.Error("PTC tooling instruction should be locked")
			}
			if p.EntryContent.Content == "" {
				t.Error("PTC tooling instruction should carry content")
			}
		}
	}
	if !found {
		t.Fatal("PTC mode should seed the coding-ptc-tooling instruction program")
	}
}

// TestNormalModeDoesNotSeedPTCInstruction verifies the PTC instruction is only
// present in PTC mode.
func TestNormalModeDoesNotSeedPTCInstruction(t *testing.T) {
	w := newCoding(t, ToolModeNormal)
	for _, p := range w.ProgramsLocked() {
		if p.Name == "coding-ptc-tooling" {
			t.Fatal("normal mode must not seed the PTC instruction")
		}
	}
}
