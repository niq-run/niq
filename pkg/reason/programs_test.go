package reason

import (
	"encoding/json"
	"testing"

	"github.com/niq-run/niq/core/program"
)

// TestProgramAddRemove exercises the runtime program mutations: pure-add
// semantics (duplicate name rejected), locked protection on remove, and
// not-found errors.
func TestProgramAddRemove(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)

	if err := w.AddProgram(program.Program{
		Meta:        program.Meta{Name: "p1", ContentType: "instruction"},
		EntryContent: program.ProgramContent{Content: "x"},
	}); err != nil {
		t.Fatalf("add p1: %v", err)
	}
	if err := w.AddProgram(program.Program{Meta: program.Meta{Name: "p1", ContentType: "instruction"}}); err == nil {
		t.Fatal("duplicate add should fail")
	}
	if err := w.RemoveProgram("nope"); err == nil {
		t.Fatal("remove of missing program should fail")
	}

	// Locked programs may be added but not removed via the meta-extension.
	if err := w.AddProgram(program.Program{Meta: program.Meta{Name: "locked", ContentType: "playbook", Locked: true}}); err != nil {
		t.Fatalf("add locked: %v", err)
	}
	if err := w.RemoveProgram("locked"); err == nil {
		t.Fatal("remove of locked program should fail")
	}

	if err := w.RemoveProgram("p1"); err != nil {
		t.Fatalf("remove p1: %v", err)
	}
	got := w.ListPrograms()
	if len(got) != 1 || got[0].Name != "locked" {
		t.Fatalf("after ops: %+v", got)
	}
}

// TestProgramSnapshotRestoreOverrides verifies the single-attribute contract:
// once a worker has a snapshot, restore unconditionally overrides config, so a
// config-only program is dropped in favour of the snapshot's list.
func TestProgramSnapshotRestoreOverrides(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.programs = []program.Program{
		{Meta: program.Meta{Name: "cfg-only", ContentType: "instruction"}, EntryContent: program.ProgramContent{Content: "c"}},
	}
	if err := w.AddProgram(program.Program{Meta: program.Meta{Name: "runtime", ContentType: "playbook"}}); err != nil {
		t.Fatal(err)
	}
	snap, err := w.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	w2 := newTestWorker(&staticProvider{}, nil)
	w2.programs = []program.Program{
		{Meta: program.Meta{Name: "other-cfg", ContentType: "instruction"}, EntryContent: program.ProgramContent{Content: "o"}},
	}
	if err := w2.Restore(snap); err != nil {
		t.Fatal(err)
	}

	got := w2.ListPrograms()
	if len(got) != 2 {
		t.Fatalf("restore should override config; got %d programs: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name] = true
	}
	if !names["cfg-only"] || !names["runtime"] {
		t.Fatalf("restore lost a snapshot program: %+v", got)
	}
	if names["other-cfg"] {
		t.Fatalf("restore did not override config: %+v", got)
	}
}

// TestProgramRestoreKeepsConfigWhenAbsent verifies backward compat: a snapshot
// taken before the Programs field existed (field absent → nil) leaves config
// standing rather than wiping it.
func TestProgramRestoreKeepsConfigWhenAbsent(t *testing.T) {
	w0 := newTestWorker(&staticProvider{}, nil)
	snap0, err := w0.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	// Strip the Programs field, simulating a pre-change snapshot.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(snap0, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "programs")
	stripped, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	w := newTestWorker(&staticProvider{}, nil)
	w.programs = []program.Program{
		{Meta: program.Meta{Name: "cfg", ContentType: "instruction"}, EntryContent: program.ProgramContent{Content: "c"}},
	}
	if err := w.Restore(stripped); err != nil {
		t.Fatal(err)
	}
	got := w.ListPrograms()
	if len(got) != 1 || got[0].Name != "cfg" {
		t.Fatalf("old snapshot should keep config: %+v", got)
	}
}
