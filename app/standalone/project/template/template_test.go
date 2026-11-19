package template

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSeedTemplates verifies first-run seeding populates an empty templates
// dir from the built-ins and is idempotent (does not reseed / clobber).
func TestSeed(t *testing.T) {
	dir := t.TempDir()
	if err := Seed(dir); err != nil {
		t.Fatalf("SeedTemplates: %v", err)
	}
	// The default template should be present, as a template directory.
	raw, err := os.ReadFile(filepath.Join(dir, "default", "template.json"))
	if err != nil {
		t.Fatalf("expected default/template.json after seed: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("seeded default template.json is empty")
	}
	names, _ := ListIn(dir)
	if len(names) != 1 || names[0] != "default" {
		t.Fatalf("ListTemplatesIn = %v, want [default]", names)
	}

	// Seed again with a prior present dir: must not write anything new or error.
	before, _ := os.ReadDir(dir)
	if err := Seed(dir); err != nil {
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
		for _, k := range []string{"provider", "model", "instruction"} {
			if _, ok := w.Params[k]; ok {
				t.Fatalf("default template should not carry %q, got %+v", k, w.Params)
			}
		}
	}

	// Not seeded: falls back to embedded.
	emptyDir := t.TempDir()
	fromEmbed, err := Load(emptyDir, "default")
	if err != nil {
		t.Fatalf("Load(unseeded, default): %v", err)
	}
	if len(fromEmbed.Workers) != len(def.Workers) {
		t.Fatalf("fallback template workers = %d, want %d", len(fromEmbed.Workers), len(def.Workers))
	}

	// Seed, then overwrite default/template.json on disk with a minimal,
	// distinct template.
	if err := Seed(dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	minimal := `{"schema_version":2,"workers":[{"type":"reason","id":"solo"}]}`
	if err := os.WriteFile(filepath.Join(dir, "default", "template.json"), []byte(minimal), 0644); err != nil {
		t.Fatal(err)
	}
	fromDisk, err := Load(dir, "default")
	if err != nil {
		t.Fatalf("Load(disk, default): %v", err)
	}
	if len(fromDisk.Workers) != 1 || fromDisk.Workers[0].ID != "solo" {
		t.Fatalf("disk template not preferred: %+v", fromDisk.Workers)
	}
}
