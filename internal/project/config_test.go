package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/event"
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

// TestSubscriptionSpecParsing verifies a subscription entry accepts both the
// bare type string and the {"type","source"} object form.
func TestSubscriptionSpecParsing(t *testing.T) {
	raw := `{"workers":[{"type":"reason","id":"r","subscriptions":[
		"worker.discover",
		{"type":"request.completed","source":"timer"}
	]}]}`
	cfg, err := parseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	subs := cfg.Workers[0].Subscriptions
	if len(subs) != 2 {
		t.Fatalf("subscriptions = %+v, want 2 entries", subs)
	}
	if subs[0].Type != "worker.discover" || subs[0].Source != "" {
		t.Fatalf("string entry = %+v, want type-only worker.discover", subs[0])
	}
	if subs[1].Type != "request.completed" || subs[1].Source != "timer" {
		t.Fatalf("object entry = %+v, want request.completed from timer", subs[1])
	}

	// Round-trip through workerConfigParams → subscriptionPatterns must yield
	// the same EventPatterns, source included.
	p := workerConfigParams(cfg.Workers[0])
	patterns := subscriptionPatterns(p["subscriptions"])
	if len(patterns) != 2 {
		t.Fatalf("patterns = %+v, want 2", patterns)
	}
	if patterns[0].Type != "worker.discover" || patterns[0].SourceID != "" {
		t.Fatalf("pattern[0] = %+v, want worker.discover", patterns[0])
	}
	if patterns[1].Type != "request.completed" || patterns[1].SourceID != "timer" {
		t.Fatalf("pattern[1] = %+v, want request.completed/timer", patterns[1])
	}
	if !patterns[1].Matches(event.New("request.completed", "timer", nil)) {
		t.Fatal("pattern[1] should match request.completed from timer")
	}
	if patterns[1].Matches(event.New("request.completed", "niq", nil)) {
		t.Fatal("pattern[1] must reject request.completed from another source")
	}
}

// TestSubAllowFromParams verifies the template-first / hardcoded-default rule.
func TestSubAllowFromParams(t *testing.T) {
	// Configured subscriptions win, source preserved.
	cfg, err := parseConfig([]byte(`{"workers":[{"type":"reason","id":"r","subscriptions":["worker.ready",{"type":"request.completed","source":"niq"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := subAllowFromParams(workerConfigParams(cfg.Workers[0]), []string{"worker.discover"})
	if len(got) != 2 || got[0].Type != "worker.ready" || got[1].SourceID != "niq" {
		t.Fatalf("configured subs not honored: %+v", got)
	}

	// No config → hardcoded defaults.
	empty, _ := parseConfig([]byte(`{"workers":[{"type":"reason","id":"r"}]}`))
	got = subAllowFromParams(workerConfigParams(empty.Workers[0]), []string{"worker.discover"})
	if len(got) != 1 || got[0].Type != "worker.discover" {
		t.Fatalf("defaults not used: %+v", got)
	}
}
