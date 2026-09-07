package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMountsParamTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	p := map[string]any{
		"mounts": []any{
			"~/.niq/projects/alpha/workspace",
			"~/uploads",
			"/abs/path",
			"~",
		},
	}
	mounts, err := parseMountsParam(p)
	if err != nil {
		t.Fatalf("parseMountsParam: %v", err)
	}
	want := []string{
		filepath.Join(home, ".niq", "projects", "alpha", "workspace"),
		filepath.Join(home, "uploads"),
		"/abs/path",
		home,
	}
	if len(mounts) != len(want) {
		t.Fatalf("got %d mounts, want %d: %v", len(mounts), len(want), mounts)
	}
	for i, m := range mounts {
		if m != want[i] {
			t.Errorf("mount[%d] = %q, want %q", i, m, want[i])
		}
	}
}

func TestParseMountsParamPathSugar(t *testing.T) {
	p := map[string]any{"path": "~/.niq/projects/beta/programs"}
	mounts, err := parseMountsParam(p)
	if err != nil {
		t.Fatalf("parseMountsParam: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".niq", "projects", "beta", "programs")
	if len(mounts) != 1 || mounts[0] != want {
		t.Fatalf("mounts = %v, want [%s]", mounts, want)
	}
}
