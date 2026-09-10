package project

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListPrograms verifies ListPrograms enumerates a project's programs from
// its programs/ directory — and yields an empty list with no program space.
func TestListPrograms(t *testing.T) {
	setupProjectsRoot(t)

	// No project or programs → empty, not an error.
	ps, err := ListPrograms("alpha")
	if err != nil {
		t.Fatalf("ListPrograms on missing project: %v", err)
	}
	if len(ps) != 0 {
		t.Fatalf("expected empty list for missing project, got %d", len(ps))
	}

	progDir := ProjectProgramsDir("alpha")
	if err := os.MkdirAll(progDir, 0755); err != nil {
		t.Fatal(err)
	}
	review := filepath.Join(progDir, "review")
	if err := os.MkdirAll(review, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(review, "SKILL.md"), []byte("---\nname: review\ndescription: code review\ncontent_type: playbook\ntags: [code, review]\n---\n# Review\n\nBody.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// A second program under a nested directory is discovered too.
	strict := filepath.Join(progDir, "nest", "strict")
	if err := os.MkdirAll(strict, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(strict, "PROGRAM.md"), []byte("---\nname: strict\ncontent_type: instruction\n---\nNever fail.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ps, err = ListPrograms("alpha")
	if err != nil {
		t.Fatalf("ListPrograms: %v", err)
	}
	// Sorted by name: review, strict.
	if len(ps) != 2 {
		t.Fatalf("expected 2 programs, got %d", len(ps))
	}
	first := ps[0]
	if first.Name != "review" {
		t.Fatalf("first program = %q, want review", first.Name)
	}
	if first.Description != "code review" || len(first.Tags) != 2 {
		t.Fatalf("review meta unparsed: %+v", first)
	}
	if first.FormType == "" {
		t.Fatalf("review entry form_type not captured: %+v", first)
	}
	if ps[1].ContentType != "instruction" {
		t.Fatalf("strict content_type = %q, want instruction", ps[1].ContentType)
	}
}