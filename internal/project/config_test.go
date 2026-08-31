package project

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSeedTemplates verifies first-run seeding populates an empty templates
// dir from the built-ins and is idempotent (does not reseed / clobber).
func TestSeedTemplates(t *testing.T) {
	dir := t.TempDir()
	if err := SeedTemplates(dir); err != nil {
		t.Fatalf("SeedTemplates: %v", err)
	}
	// The default template should be present, as JSON.
	raw, err := os.ReadFile(filepath.Join(dir, "default.json"))
	if err != nil {
		t.Fatalf("expected default.json after seed: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("seeded default.json is empty")
	}

	// Seed again with a prior present dir: must not write anything new or error.
	before, _ := os.ReadDir(dir)
	if err := SeedTemplates(dir); err != nil {
		t.Fatalf("second SeedTemplates: %v", err)
	}
	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Fatalf("re-seed changed templates dir: %d -> %d files", len(before), len(after))
	}
}

// TestLoadTemplatePrefersDisk verifies a user-edited on-disk template wins over
// the embedded built-in, and that a missing disk file falls back to the built-in.
func TestLoadTemplatePrefersDisk(t *testing.T) {
	dir := t.TempDir()
	def, err := LoadPreset("default")
	if err != nil {
		t.Fatalf("LoadPreset(default): %v", err)
	}
	if len(def.Workers) == 0 {
		t.Fatal("default preset unexpectedly empty")
	}
	// The default template carries no provider/model/instruction: those come from
	// providers.json / the instruction system, not the template.
	for _, w := range def.Workers {
		if w.Provider != "" || w.Model != "" || w.Instruction != "" {
			t.Fatalf("default template should not pin provider/model/instruction, got %+v", w)
		}
	}

	// Not seeded: falls back to embedded.
	emptyDir := t.TempDir()
	fromEmbed, err := LoadTemplate(emptyDir, "default")
	if err != nil {
		t.Fatalf("LoadTemplate(unseeded, default): %v", err)
	}
	if len(fromEmbed.Workers) != len(def.Workers) {
		t.Fatalf("fallback template workers = %d, want %d", len(fromEmbed.Workers), len(def.Workers))
	}

	// Seed, then overwrite default.json on disk with a minimal, distinct template.
	if err := SeedTemplates(dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	minimal := `{"workers":[{"type":"reason","id":"solo"}]}`
	if err := os.WriteFile(filepath.Join(dir, "default.json"), []byte(minimal), 0644); err != nil {
		t.Fatal(err)
	}
	fromDisk, err := LoadTemplate(dir, "default")
	if err != nil {
		t.Fatalf("LoadTemplate(disk, default): %v", err)
	}
	if len(fromDisk.Workers) != 1 || fromDisk.Workers[0].ID != "solo" {
		t.Fatalf("disk template not preferred: %+v", fromDisk.Workers)
	}
}
