package secret

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Default is the provider the standalone form uses today: `env:` and `file:`.
// A dedicated `vault:` provider plugs in behind the same interface later; the
// assembly injects whichever one it wants.
type Default struct{}

var _ Provider = Default{}

// Resolve expands `env:NAME` and `file:PATH`; anything else is a literal.
//
// An unreadable `file:` reference falls through to the reference itself, which
// is the behaviour this helper has always had: the connect then fails on the
// auth check rather than silently presenting a path as a token.
func (Default) Resolve(ref string) string {
	if name, ok := strings.CutPrefix(ref, "env:"); ok {
		return os.Getenv(name)
	}
	if path, ok := strings.CutPrefix(ref, "file:"); ok {
		if b, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ref
}

// Store writes a value to a `file:` reference (mode 0600). `env:` is read-only.
func (Default) Store(ref, value string) error {
	path, ok := strings.CutPrefix(ref, "file:")
	if !ok {
		if strings.HasPrefix(ref, "env:") {
			return fmt.Errorf("secret: %q is read-only (the environment is not ours to write)", ref)
		}
		return fmt.Errorf("secret: unsupported reference %q (use file:PATH)", ref)
	}
	if path == "" {
		return fmt.Errorf("secret: empty file: path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("secret: create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(value)+"\n"), 0o600); err != nil {
		return fmt.Errorf("secret: write %s: %w", path, err)
	}
	return nil
}
