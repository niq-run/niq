package niqhome

import (
	"os"
	"path/filepath"
	"testing"
)

// withEnv runs f with NIQ_HOME unset, so tests never inherit a stray value.
func withEnv(t *testing.T, f func()) {
	t.Helper()
	_ = os.Unsetenv(Env)
	t.Cleanup(func() { _ = os.Unsetenv(Env) })
	f()
}

func TestRootDefaultsToDotNiq(t *testing.T) {
	withEnv(t, func() {
		if os.Getenv(Env) != "" {
			t.Fatal("NIQ_HOME should be unset in this test")
		}
		home, _ := os.UserHomeDir()
		if got, want := Root(), filepath.Join(home, ".niq"); got != want {
			t.Fatalf("Root() = %q, want %q", got, want)
		}
	})
}

func TestRootHonorsNIQHome(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "my-niq-root")
	t.Setenv(Env, custom)
	if got := Root(); got != custom {
		t.Fatalf("Root() = %q, want %q", got, custom)
	}
}

func TestRootExpandsTilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	t.Setenv(Env, "~/.portable-niq")
	if got, want := Root(), filepath.Join(home, ".portable-niq"); got != want {
		t.Fatalf("Root() = %q, want %q", got, want)
	}
}

func TestRootCleansValue(t *testing.T) {
	t.Setenv(Env, filepath.Join(t.TempDir(), "a", "b", "..")+string(filepath.Separator))
	if got := Root(); !filepath.IsAbs(got) || filepath.Base(got) == ".." || filepath.Base(got) == "b" {
		t.Fatalf("Root() = %q, want a cleaned path", got)
	}
}
