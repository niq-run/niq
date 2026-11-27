package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveExpandsEnvAndFile(t *testing.T) {
	t.Setenv("NIQ_SECRET_TEST", "tok-env")

	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("  tok-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := Default{}
	if got := p.Resolve("env:NIQ_SECRET_TEST"); got != "tok-env" {
		t.Errorf("env: = %q", got)
	}
	if got := p.Resolve("file:" + path); got != "tok-file" {
		t.Errorf("file: = %q, want the trimmed file content", got)
	}
	if got := p.Resolve("plain-token"); got != "plain-token" {
		t.Errorf("literal = %q, want it unchanged", got)
	}
	if got := p.Resolve(""); got != "" {
		t.Errorf("empty = %q", got)
	}
}

func TestResolveMissingFileFallsThrough(t *testing.T) {
	ref := "file:/definitely/not/here/token"
	if got := (Default{}).Resolve(ref); got != ref {
		t.Errorf("Resolve(%q) = %q, want the reference itself", ref, got)
	}
}

func TestStoreFileRoundTrips(t *testing.T) {
	p := Default{}
	path := filepath.Join(t.TempDir(), "nested", "token")
	ref := "file:" + path

	if err := p.Store(ref, "  issued-token  "); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if got := p.Resolve(ref); got != "issued-token" {
		t.Errorf("round trip = %q", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestStoreRejectsUnwritableSchemes(t *testing.T) {
	p := Default{}
	if err := p.Store("env:FOO", "x"); err == nil {
		t.Error("env: must be read-only")
	} else if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("error = %v", err)
	}
	if err := p.Store("vault:later", "x"); err == nil {
		t.Error("an unimplemented scheme must be rejected, not silently accepted")
	}
	if err := p.Store("file:", "x"); err == nil {
		t.Error("an empty file: path must be rejected")
	}
}
