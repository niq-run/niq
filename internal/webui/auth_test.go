package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsLocalOnlyAddr verifies loopback-bound detection across common address
// forms.
func TestIsLocalOnlyAddr(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9527", true},
		{"localhost:9527", false}, // conservative: unresolvable name → not local-only
		{":9527", false},          // wildcard → remote-reachable
		{"0.0.0.0:9527", false},
		{"[::]:9527", false},
		{"192.168.1.10:9527", false},
		{"[::1]:9527", true},
	} {
		if got := IsLocalOnlyAddr(tc.addr); got != tc.want {
			t.Fatalf("IsLocalOnlyAddr(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// TestStartupAuthSpecPersistsAndReloads verifies a spec writes credentials to
// the auth file and a later empty spec reloads them.
func TestStartupAuthSpecPersistsAndReloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".niq", "common", "auth.json")

	// Providing a spec persists it, enabled, no warning (remote-reachable addr
	// but auth configured).
	u, p, enabled, warning := StartupAuth("0.0.0.0:9527", "alice:s3cret", path)
	if !enabled || u != "alice" || p != "s3cret" {
		t.Fatalf("set spec: (%q,%q,%v), want enabled alice/s3cret", u, p, enabled)
	}
	if warning != "" {
		t.Fatalf("auth configured: unexpected warning %q", warning)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("auth file not written: %v", err)
	}
	if got := string(b); !strings.Contains(got, `"user": "alice"`) || !strings.Contains(got, `"pass": "s3cret"`) {
		t.Fatalf("auth file content: %s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat auth file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("auth file perm = %o, want 600", perm)
	}

	// Empty spec now reloads from the file.
	if u, p, enabled, _ = StartupAuth("0.0.0.0:9527", "", path); !enabled || u != "alice" || p != "s3cret" {
		t.Fatalf("reload: (%q,%q,%v), want enabled alice/s3cret", u, p, enabled)
	}

	// A new spec overwrites the file.
	if _, _, enabled, _ = StartupAuth("0.0.0.0:9527", "bob:hunter2", path); !enabled {
		t.Fatal("second set should enable")
	}
	if u, p, enabled, _ = StartupAuth("0.0.0.0:9527", "", path); !enabled || u != "bob" || p != "hunter2" {
		t.Fatalf("reload after overwrite: (%q,%q,%v)", u, p, enabled)
	}
}

// TestStartupAuthLoopbackNoAuthNoWarning verifies a loopback-bound server with
// no auth configured starts clean (nothing forced) and emits no warning.
func TestStartupAuthLoopbackNoAuthNoWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".niq", "common", "auth.json")

	u, p, enabled, warning := StartupAuth("127.0.0.1:1234", "", path)
	if enabled {
		t.Fatalf("expected no auth on loopback, got enabled (%q,%q)", u, p)
	}
	if warning != "" {
		t.Fatalf("loopback no-auth should not warn, got %q", warning)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("loopback no-auth must not write auth file (stat err=%v)", err)
	}
}

// TestStartupAuthRemoteNoAuthWarnsButNotForced verifies a remote-reachable bind
// with no auth configured produces a local-only warning and is NOT forced
// (disabled, warning, no file write).
func TestStartupAuthRemoteNoAuthWarnsButNotForced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".niq", "common", "auth.json")

	u, p, enabled, warning := StartupAuth("0.0.0.0:9527", "", path)
	if enabled {
		t.Fatalf("expected no auth, got enabled (%q,%q)", u, p)
	}
	if warning == "" {
		t.Fatal("expected a local-only/needs-config warning for remote bind without auth")
	}
	if !strings.Contains(warning, "only local access works") || !strings.Contains(warning, path) {
		t.Fatalf("warning should mention local-only and the auth path: %q", warning)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("no-auth start must not write auth file (stat err=%v)", err)
	}
}

// TestStartupAuthInvalidSpecDisabled verifies a malformed spec neither enables
// auth nor writes a file.
func TestStartupAuthInvalidSpecDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".niq", "common", "auth.json")

	_, _, enabled, _ := StartupAuth("0.0.0.0:9527", ":only-pass-missing-user", path)
	if enabled {
		t.Fatal("invalid spec should stay disabled")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid spec should not write auth file (stat err=%v)", err)
	}
}
