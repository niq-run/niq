package swarm

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
	// At least the dev template should be present.
	raw, err := os.ReadFile(filepath.Join(dir, "dev.yaml"))
	if err != nil {
		t.Fatalf("expected dev.yaml after seed: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("seeded dev.yaml is empty")
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
	dev, err := LoadPreset("dev")
	if err != nil {
		t.Fatalf("LoadPreset(dev): %v", err)
	}
	if len(dev.Workers) == 0 {
		t.Fatal("dev preset unexpectedly empty")
	}

	// Not seeded: falls back to embedded.
	emptyDir := t.TempDir()
	fromEmbed, err := LoadTemplate(emptyDir, "dev")
	if err != nil {
		t.Fatalf("LoadTemplate(unseeded, dev): %v", err)
	}
	if len(fromEmbed.Workers) != len(dev.Workers) {
		t.Fatalf("fallback template workers = %d, want %d", len(fromEmbed.Workers), len(dev.Workers))
	}

	// Seed, then overwrite dev.yaml on disk with a minimal, distinct template.
	if err := SeedTemplates(dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	minimal := "workers:\n  - type: reason\n    id: solo\n"
	if err := os.WriteFile(filepath.Join(dir, "dev.yaml"), []byte(minimal), 0644); err != nil {
		t.Fatal(err)
	}
	fromDisk, err := LoadTemplate(dir, "dev")
	if err != nil {
		t.Fatalf("LoadTemplate(disk, dev): %v", err)
	}
	if len(fromDisk.Workers) != 1 || fromDisk.Workers[0].ID != "solo" {
		t.Fatalf("disk template not preferred: %+v", fromDisk.Workers)
	}
}