package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Fallback is the non-vault half of the provider: the `env:` and `file:`
// references, with the behaviour this codebase has always had. Keeping it as a
// separate type means a deployment that only wants environment variables can
// wire Fallback alone, and Vault composes it for everything that is not
// `vault:`.
type Fallback struct{}

var _ Provider = Fallback{}

// Resolve expands `env:NAME` and `file:PATH`; anything else is a literal.
//
// An unreadable `file:` reference falls through to the reference itself, which
// is the long-standing behaviour: the caller's connect fails on the auth check
// rather than silently presenting a path as a token.
func (Fallback) Resolve(ref string) string {
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

// Store writes a value to a `file:` reference (mode 0600). `env:` is read-only,
// and vault: is not this provider's to write.
func (Fallback) Store(ref, value string) error {
	path, ok := strings.CutPrefix(ref, "file:")
	if !ok {
		switch {
		case strings.HasPrefix(ref, "env:"):
			return fmt.Errorf("vault: %q is read-only (the environment is not ours to write)", ref)
		case strings.HasPrefix(ref, "vault:"):
			return fmt.Errorf("vault: %q needs the vault provider, not the fallback", ref)
		default:
			return fmt.Errorf("vault: unsupported reference %q (use vault:NAME or file:PATH)", ref)
		}
	}
	if path == "" {
		return fmt.Errorf("vault: empty file: path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("vault: create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(value)+"\n"), 0o600); err != nil {
		return fmt.Errorf("vault: write %s: %w", path, err)
	}
	return nil
}
