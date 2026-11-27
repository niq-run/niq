// Package vault is the standalone form's credential store: one shared,
// user-level place that worker params and the model-provider config reference by
// name.
//
// It lives at the standalone layer because credentials are a user resource, not
// a project one: <root>/common/vault, next to providers/. A project only names
// an entry; the value is shared by every project on the machine.
//
// Layout: one file per entry, <root>/common/vault/<name>, mode 0600. A name may
// contain slashes to group entries (`openai/main`), but that is a VIEW, not a
// hierarchy: there are no per-scope permissions, and a slash is just a character
// in the name. Grouping by prefix is how a scoped view is derived.
//
// Plaintext at rest, deliberately for now: the file is 0600 under the user's own
// root, the same trust level as provider.json. A sealed backend (OS keychain, a
// database) replaces the storage behind this package later; this API is the seam
// — one entry per name, no storage detail leaks out.
package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Provider is what the assembly hands to the code that BUILDS workers (and to
// the control plane that expands managed-process params). A worker never sees
// it: it receives plain values, or a bare resolver function where a runtime
// value (a dialled credential) must be expanded on the spot.
type Provider interface {
	// Resolve expands a reference: `vault:NAME`, `env:NAME`, `file:PATH`. A value
	// with no known scheme is a literal and is returned unchanged.
	Resolve(ref string) string
	// Store writes value at ref. `vault:` and `file:` are writable; `env:` is not.
	Store(ref, value string) error
}

// Entry is one credential's metadata. It deliberately carries no value: a list
// is for management UIs, and a UI must never receive the secret itself.
type Entry struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

var (
	// ErrNotFound reports a name that is not in the vault.
	ErrNotFound = errors.New("vault: no such credential")
	// ErrBadName reports a name that cannot be a file under the vault root.
	ErrBadName = errors.New("vault: invalid credential name")
)

// Vault is the file-backed store.
type Vault struct {
	dir      string
	fallback Provider
}

var _ Provider = (*Vault)(nil)

// New opens the vault rooted at dir. The directory is created on first write.
func New(dir string) *Vault {
	return &Vault{dir: dir, fallback: Fallback{}}
}

// Dir is the root this vault stores entries under.
func (v *Vault) Dir() string { return v.dir }

// Resolve implements Provider. `vault:NAME` reads the entry; every other scheme
// goes to the fallback (env/file/literal).
//
// A missing entry resolves to the reference itself, matching the behaviour the
// env/file provider has always had: the caller's connect fails on the auth
// check rather than silently presenting a name as a token.
func (v *Vault) Resolve(ref string) string {
	name, ok := strings.CutPrefix(ref, "vault:")
	if !ok {
		return v.fallback.Resolve(ref)
	}
	b, err := os.ReadFile(v.path(name))
	if err != nil {
		return ref
	}
	return strings.TrimSpace(string(b))
}

// Store implements Provider. `vault:NAME` writes the entry; `file:` and `env:`
// are delegated so one call handles any reference a caller may hold.
func (v *Vault) Store(ref, value string) error {
	if name, ok := strings.CutPrefix(ref, "vault:"); ok {
		return v.Put(name, value)
	}
	return v.fallback.Store(ref, value)
}

// Put writes (or overwrites) one entry, mode 0600. It is idempotent: storing the
// same name again replaces it, which is what makes "seal this literal" safe to
// re-run.
func (v *Vault) Put(name, value string) error {
	path, err := v.pathE(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("vault: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(value)+"\n"), 0o600); err != nil {
		return fmt.Errorf("vault: write %s: %w", name, err)
	}
	return nil
}

// Get returns one entry's value.
func (v *Vault) Get(name string) (string, error) {
	b, err := os.ReadFile(v.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("vault: read %s: %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Has reports whether an entry exists.
func (v *Vault) Has(name string) bool {
	_, err := os.Stat(v.path(name))
	return err == nil
}

// List returns every entry's metadata (never its value), sorted by name.
func (v *Vault) List() ([]Entry, error) {
	var out []Entry
	err := filepath.WalkDir(v.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // nothing stored yet
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(v.dir, path)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil // raced with a delete; skip
		}
		out = append(out, Entry{
			Name:      filepath.ToSlash(rel),
			Size:      info.Size(),
			UpdatedAt: info.ModTime().UTC(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("vault: list: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Delete removes one entry. A worker being destroyed must NOT delete its
// credentials: an entry may be shared, and a worker is not the owner of a value
// it merely references. Deletion is an explicit cache-management action.
func (v *Vault) Delete(name string) error {
	path, err := v.pathE(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("vault: delete %s: %w", name, err)
	}
	return nil
}

// path is the storage path for name, for reads: an invalid name can only fail to
// exist.
func (v *Vault) path(name string) string {
	path, err := v.pathE(name)
	if err != nil {
		return ""
	}
	return path
}

// pathE validates the name and resolves it under the root.
func (v *Vault) pathE(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	path := filepath.Join(v.dir, filepath.FromSlash(name))
	// Defence in depth: validation already rejects "..", but the joined path must
	// still be under the root, not merely look like it.
	if !strings.HasPrefix(path, filepath.Clean(v.dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q escapes the vault root", ErrBadName, name)
	}
	return path, nil
}

var segmentRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateName accepts slash-separated segments of letters, digits and `._-`.
// Rejecting `.`/`..` (and empty segments) is what keeps a name from being a path
// traversal; everything else is allowed, because a name is just a string.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrBadName)
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("%w: %q must not start or end with a slash", ErrBadName, name)
	}
	if len(name) > 256 {
		return fmt.Errorf("%w: %q is too long", ErrBadName, name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." || !segmentRE.MatchString(seg) {
			return fmt.Errorf("%w: %q has an invalid segment %q", ErrBadName, name, seg)
		}
	}
	return nil
}

// IsRef reports whether value already names a secret rather than carrying one.
// This is the predicate the write path uses before sealing a literal: an
// existing reference is never re-sealed.
func IsRef(value string) bool {
	for _, scheme := range []string{"vault:", "env:", "file:"} {
		if strings.HasPrefix(value, scheme) {
			return true
		}
	}
	return false
}
