package reason

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/core/program"
)

// TestBuildInstructionEmpty verifies a worker with no programs still gets a
// valid system prompt identifying the worker.
func TestBuildInstructionEmpty(t *testing.T) {
	w := NewBaseReasonWorker(Config{ID: "r1", Bus: newTestChannel()})
	s := w.buildInstruction()
	if !strings.Contains(s, "r1") {
		t.Fatalf("prompt should mention worker ID r1, got: %q", s)
	}
	if !strings.Contains(s, "own focus") || !strings.Contains(s, "send_message") {
		t.Fatalf("prompt should describe focus and worker collaboration: %q", s)
	}
}

// TestBuildInstructionInstruction verifies instruction programs contribute
// their full content, and locked ones are marked.
func TestBuildInstructionInstruction(t *testing.T) {
	w := NewBaseReasonWorker(Config{ID: "r1", Bus: newTestChannel(), Programs: []program.Program{
		{
			Meta:         program.Meta{Name: "identity", ContentType: program.ContentTypeInstruction},
			EntryContent: program.ProgramContent{Content: "You are a code assistant."},
		},
		{
			Meta:         program.Meta{Name: "policy", ContentType: program.ContentTypeInstruction, Locked: true},
			EntryContent: program.ProgramContent{Content: "Never delete data."},
		},
	}})
	s := w.buildInstruction()
	if !strings.Contains(s, "You are a code assistant.") {
		t.Fatalf("instruction content missing: %q", s)
	}
	if !strings.Contains(s, "[locked]") || !strings.Contains(s, "Never delete data.") {
		t.Fatalf("locked instruction should be marked [locked]: %q", s)
	}
}

// TestBuildInstructionPlaybook verifies playbook programs contribute only
// metadata (name/description/tags), not full content.
func TestBuildInstructionPlaybook(t *testing.T) {
	w := NewBaseReasonWorker(Config{ID: "r1", Bus: newTestChannel(), Programs: []program.Program{
		{
			Meta: program.Meta{Name: "code-review", ContentType: program.ContentTypePlaybook,
				Description: "Review code", Tags: []string{"go", "review"}},
			EntryContent: program.ProgramContent{Content: "STEP: run linter"},
		},
	}})
	s := w.buildInstruction()
	if !strings.Contains(s, "code-review") || !strings.Contains(s, "Review code") {
		t.Fatalf("playbook metadata missing: %q", s)
	}
	if strings.Contains(s, "STEP: run linter") {
		t.Fatalf("playbook content should not be injected: %q", s)
	}
}
