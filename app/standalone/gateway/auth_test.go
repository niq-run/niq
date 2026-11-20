package gateway

import (
	"net/http"
	"net/http/httptest"
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

// TestStartupAuthRemoteNoAuthWarnsNotForced verifies a remote-reachable bind
// with no auth configured produces an "unauthenticated" warning and is NOT
// forced (disabled, warning, no file write): auth is opt-in.
func TestStartupAuthRemoteNoAuthWarnsNotForced(t *testing.T) {
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
	if !strings.Contains(warning, "without authentication") || !strings.Contains(warning, path) {
		t.Fatalf("warning should say the bind is unauthenticated and name the auth path: %q", warning)
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

// TestBasicAuthNonLoopback verifies the gate: loopback peers pass without
// credentials, remote peers get a challenge, and correct credentials pass.
func TestBasicAuthNonLoopback(t *testing.T) {
	hit := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true })
	h := BasicAuth("alice", "s3cret", next)

	call := func(remote string, creds bool) *httptest.ResponseRecorder {
		hit = false
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		if creds {
			req.SetBasicAuth("alice", "s3cret")
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	if rr := call("127.0.0.1:5555", false); !hit || rr.Code != http.StatusOK {
		t.Fatalf("loopback: hit=%v code=%d, want hit with 200", hit, rr.Code)
	}
	if rr := call("203.0.113.7:5555", false); hit || rr.Code != http.StatusUnauthorized {
		t.Fatalf("no-cred: hit=%v code=%d, want reject with 401", hit, rr.Code)
	} else if rr.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("no-cred: missing WWW-Authenticate header")
	}
	if rr := call("203.0.113.7:5555", true); !hit || rr.Code != http.StatusOK {
		t.Fatalf("good-cred: hit=%v code=%d, want hit with 200", hit, rr.Code)
	}
}

// TestBasicAuthUnconfigured asserts auth is opt-in: with no credentials the
// gate lets every caller through.
func TestBasicAuthUnconfigured(t *testing.T) {
	hit := false
	h := BasicAuth("", "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !hit || rr.Code != http.StatusOK {
		t.Fatalf("hit=%v code=%d, want pass-through 200", hit, rr.Code)
	}
}

func TestParseAuthSpec(t *testing.T) {
	for _, tc := range []struct {
		spec, wantUser, wantPass string
		wantOK                   bool
	}{
		{"", "", "", false},
		{"  ", "", "", false},
		{"s3cret", "niq", "s3cret", true},
		{"alice:s3cret", "alice", "s3cret", true},
		{":s3cret", "", "", false},
		{"alice:", "", "", false},
	} {
		u, p, ok := ParseAuthSpec(tc.spec)
		if u != tc.wantUser || p != tc.wantPass || ok != tc.wantOK {
			t.Fatalf("ParseAuthSpec(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.spec, u, p, ok, tc.wantUser, tc.wantPass, tc.wantOK)
		}
	}
}
