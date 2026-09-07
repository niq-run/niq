// Package pgbackend provides a filesystem-based implementation of
// program.Backend using wsbackend.EmbeddedBackend.
//
// It stores each Program as a directory whose entry file — PROGRAM.md, or
// SKILL.md so a mainstream agent's skill directory can be dropped in
// unchanged — carries YAML frontmatter followed by the content body. Other
// files in the directory are the Program's sub-contents.
//
// The name → directory mapping is private to this package. Discovery walks
// the mount recursively so a Program may live at any depth, but callers only
// ever see the abstract address "{name}" and never learn where the directory
// actually is. The same goes for storage format: callers hand over and
// receive content, never serialisation.
package pgbackend

import (
	"context"
	"fmt"
	"log"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/niq-run/niq/core/program"
	"github.com/niq-run/niq/pkg/services/wsbackend"
	"gopkg.in/yaml.v3"
)

// entryFileNames lists the recognized entry file names, in preference order.
// PROGRAM.md is niq's own; SKILL.md is the mainstream agent skill convention.
var entryFileNames = []string{"PROGRAM.md", "SKILL.md"}

// Backend adapts wsbackend.EmbeddedBackend to program.Backend.
//
// It keeps a name → directory index so single-address operations (Read, Edit,
// Remove) resolve without walking the mount. List always walks afresh and
// refreshes the index; Upsert and Remove invalidate it; a lookup miss
// triggers one refresh, so a Program added straight to disk is still found.
type Backend struct {
	be *wsbackend.EmbeddedBackend

	mu    sync.Mutex
	index map[string]string // program name → directory, relative to the mount
}

var _ program.Backend = (*Backend)(nil)

// New returns a new Backend rooted at dir (a single mount).
func New(dir string) *Backend {
	return &Backend{be: wsbackend.NewEmbeddedBackend([]string{dir})}
}

// discovered is one Program found on disk: the name it answers to and the
// directory holding it.
type discovered struct {
	name      string
	dir       string
	entryName string
	meta      program.Meta
}

// List enumerates every Program on disk. Each carries abstract addresses
// only — the directory it was found in stays internal. Implements
// [program.Backend].
func (b *Backend) List(ctx context.Context) ([]*program.Program, error) {
	found, err := b.discover(ctx)
	if err != nil {
		return nil, err
	}
	b.reindex(found)

	seen := make(map[string]struct{}, len(found))
	out := make([]*program.Program, 0, len(found))
	for _, d := range found {
		// Duplicates are already reported by reindex, which ran above.
		if _, dup := seen[d.name]; dup {
			continue
		}
		seen[d.name] = struct{}{}

		p := &program.Program{
			Meta: d.meta,
			Path: d.name,
			EntryContent: program.ProgramContent{
				Name:     d.name,
				FormType: formTypeFromPath(d.entryName),
				Path:     d.name,
			},
		}
		contents, err := b.contents(ctx, d.dir, d.name, d.entryName)
		if err != nil {
			log.Printf("[pgbackend] list contents of %s: %v", d.dir, err)
		}
		p.Contents = contents
		out = append(out, p)
	}
	return out, nil
}

// Read loads the content at an abstract address, with any frontmatter
// stripped. A Program's root path yields its entry content.
func (b *Backend) Read(ctx context.Context, addr string) (string, error) {
	dir, rel, err := b.resolve(ctx, addr)
	if err != nil {
		return "", err
	}
	p, err := b.entryOrFile(ctx, dir, rel)
	if err != nil {
		return "", err
	}
	raw, err := b.be.Read(ctx, p, 0, 0)
	if err != nil {
		return "", err
	}
	_, body, err := parseFrontmatter(raw)
	if err != nil {
		return "", err
	}
	// An empty body is legitimate — an entry file can be all frontmatter.
	// Fall back to the raw text only when there was no frontmatter to strip
	// (parseFrontmatter then returns the raw text as the body).
	if body != raw {
		return body, nil
	}
	return raw, nil
}

// Upsert persists a Program's metadata, creating it or replacing an existing
// one. The entry body is never touched — a new Program starts empty, and an
// update keeps the body it already has. Implements [program.Backend].
func (b *Backend) Upsert(ctx context.Context, p *program.Program) error {
	if p == nil || p.Name == "" {
		return fmt.Errorf("pgbackend: program name is required")
	}

	dir, _, err := b.resolve(ctx, p.Name)
	if err != nil {
		return err
	}
	// The entry file may not exist yet — that is the create case, not an
	// error. Any deeper storage problem surfaces on the Write below.
	entryName, _ := b.entryFile(ctx, dir)
	if entryName == "" {
		entryName = entryFileNames[0]
	}
	entryPath := join(dir, entryName)

	// The entry file carries the body too, and Upsert must not disturb it.
	body := ""
	if raw, readErr := b.be.Read(ctx, entryPath, 0, 0); readErr == nil {
		_, body, _ = parseFrontmatter(raw)
	}

	content, err := renderEntry(p.Meta, body)
	if err != nil {
		return err
	}
	if err := b.be.Write(ctx, entryPath, content); err != nil {
		return err
	}
	b.invalidate()
	return nil
}

// Write replaces the content at an abstract address. At a Program's root path
// it replaces the entry body and leaves the metadata alone; at any deeper path
// it writes that file. The Program must already exist. Implements
// [program.Backend].
func (b *Backend) Write(ctx context.Context, addr, content string) error {
	dir, rel, err := b.resolve(ctx, addr)
	if err != nil {
		return err
	}
	p, err := b.entryOrFile(ctx, dir, rel)
	if err != nil {
		return err
	}

	if rel != "" {
		if err := b.be.Write(ctx, p, content); err != nil {
			return err
		}
		b.invalidate()
		return nil
	}

	// The entry file also carries the metadata; keep it.
	var meta program.Meta
	if raw, readErr := b.be.Read(ctx, p, 0, 0); readErr == nil {
		meta, _, _ = parseFrontmatter(raw)
	}
	out, err := renderEntry(meta, content)
	if err != nil {
		return err
	}
	if err := b.be.Write(ctx, p, out); err != nil {
		return err
	}
	b.invalidate()
	return nil
}

// Edit performs an atomic find-and-replace within the stored content at an
// abstract address. Implements [program.Backend].
func (b *Backend) Edit(ctx context.Context, addr, oldStr, newStr string) error {
	dir, rel, err := b.resolve(ctx, addr)
	if err != nil {
		return err
	}
	p, err := b.entryOrFile(ctx, dir, rel)
	if err != nil {
		return err
	}
	return b.be.Edit(ctx, p, oldStr, newStr)
}

// Remove deletes the Program at a root address, or a single content file at a
// deeper one. Implements [program.Backend].
func (b *Backend) Remove(ctx context.Context, addr string) error {
	dir, rel, err := b.resolve(ctx, addr)
	if err != nil {
		return err
	}
	if rel == "" {
		if err := b.be.Remove(ctx, dir); err != nil {
			return err
		}
		b.invalidate()
		return nil
	}
	return b.be.Remove(ctx, join(dir, rel))
}

// ── discovery ──

// discover walks the mount recursively and returns every Program directory in
// walk order. It runs per call rather than caching, so Programs added, moved
// or edited on disk are seen immediately.
func (b *Backend) discover(ctx context.Context) ([]discovered, error) {
	var out []discovered
	if err := b.walk(ctx, ".", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *Backend) walk(ctx context.Context, dir string, out *[]discovered) error {
	entries, err := b.be.List(ctx, dir)
	if err != nil {
		return fmt.Errorf("list %q: %w", dir, err)
	}

	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		sub := join(dir, e.Name)

		entryName, err := b.entryFile(ctx, sub)
		if err != nil {
			log.Printf("[pgbackend] skip %s: %v", sub, err)
		}
		if entryName == "" {
			// Not a Program — descend into it.
			if err := b.walk(ctx, sub, out); err != nil {
				log.Printf("[pgbackend] walk %s: %v", sub, err)
			}
			continue
		}

		meta, err := b.readMeta(ctx, join(sub, entryName))
		if err != nil {
			log.Printf("[pgbackend] skip %s: %v", sub, err)
			continue
		}
		// A Program is self-contained; don't descend into it.
		*out = append(*out, discovered{
			name:      meta.Name,
			dir:       sub,
			entryName: entryName,
			meta:      meta,
		})
	}
	return nil
}

// entryFile returns the name of the entry file in dir, or "" if dir has none.
func (b *Backend) entryFile(ctx context.Context, dir string) (string, error) {
	entries, err := b.be.List(ctx, dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if slices.Contains(entryFileNames, e.Name) {
			return e.Name, nil
		}
	}
	return "", nil
}

// readMeta parses the frontmatter of the file at p, defaulting the name to
// the containing directory and the content type to playbook.
func (b *Backend) readMeta(ctx context.Context, p string) (program.Meta, error) {
	raw, err := b.be.Read(ctx, p, 0, 0)
	if err != nil {
		return program.Meta{}, err
	}
	meta, _, err := parseFrontmatter(raw)
	if err != nil {
		return program.Meta{}, fmt.Errorf("parse frontmatter in %s: %w", p, err)
	}
	if meta.Name == "" {
		meta.Name = path.Base(path.Dir(p))
	}
	if meta.ContentType == "" {
		meta.ContentType = program.ContentTypePlaybook
	}
	return meta, nil
}

// contents lists every file under dir as an abstract content path, skipping
// the entry file and not descending into nested Programs.
func (b *Backend) contents(ctx context.Context, dir, name, entryName string) ([]program.ProgramContent, error) {
	var out []program.ProgramContent
	if err := b.collect(ctx, dir, "", name, entryName, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *Backend) collect(ctx context.Context, dir, prefix, name, entryName string, out *[]program.ProgramContent) error {
	entries, err := b.be.List(ctx, dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		rel := join(prefix, e.Name)
		if e.IsDir {
			// A nested Program is not this Program's content.
			if en, err := b.entryFile(ctx, join(dir, e.Name)); err == nil && en != "" {
				continue
			}
			if err := b.collect(ctx, join(dir, e.Name), rel, name, entryName, out); err != nil {
				log.Printf("[pgbackend] collect %s: %v", rel, err)
			}
			continue
		}
		if prefix == "" && e.Name == entryName {
			continue // the entry content is EntryContent, not a sub-content
		}
		*out = append(*out, program.ProgramContent{
			Name:     name,
			FormType: formTypeFromPath(e.Name),
			Path:     join(name, rel),
		})
	}
	return nil
}

// ── addressing ──

// resolve splits an abstract address into the directory holding the Program
// and the remainder within it, going through the name → directory index
// rather than a fresh walk. A name the index does not know is taken to be a
// Program about to be created at the root, which is what Upsert needs.
func (b *Backend) resolve(ctx context.Context, addr string) (dir, rel string, err error) {
	name, rest := splitAddr(addr)
	if name == "" {
		return "", "", fmt.Errorf("pgbackend: address %q has no program name", addr)
	}

	dir, ok, err := b.dirFor(ctx, name)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return name, rest, nil
	}
	return dir, rest, nil
}

// ── name → directory index ──

// dirFor returns the directory holding the named Program. It consults the
// index, building it on first use. A miss triggers exactly one refresh, so a
// Program added straight to disk is found without a prior List.
func (b *Backend) dirFor(ctx context.Context, name string) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.index == nil {
		if err := b.reload(ctx); err != nil {
			return "", false, err
		}
	}
	if dir, ok := b.index[name]; ok {
		return dir, true, nil
	}

	// Miss — the Program may have appeared since the last refresh.
	if err := b.reload(ctx); err != nil {
		return "", false, err
	}
	dir, ok := b.index[name]
	return dir, ok, nil
}

// reload re-walks the mount and refreshes the index. Caller holds mu.
func (b *Backend) reload(ctx context.Context) error {
	found, err := b.discover(ctx)
	if err != nil {
		return err
	}
	b.reindex(found)
	return nil
}

// reindex rebuilds the index from a discovery pass. Caller holds mu.
func (b *Backend) reindex(found []discovered) {
	index := make(map[string]string, len(found))
	for _, d := range found {
		if prev, dup := index[d.name]; dup {
			log.Printf("[pgbackend] duplicate program name %q at %s and %s; keeping %s",
				d.name, prev, d.dir, prev)
			continue
		}
		index[d.name] = d.dir
	}
	b.index = index
}

// invalidate drops the index so the next lookup rebuilds it.
func (b *Backend) invalidate() {
	b.mu.Lock()
	b.index = nil
	b.mu.Unlock()
}

// entryOrFile turns (dir, rel) into a real path: an empty rel means the
// Program's entry file.
func (b *Backend) entryOrFile(ctx context.Context, dir, rel string) (string, error) {
	if rel != "" {
		return join(dir, rel), nil
	}
	entryName, err := b.entryFile(ctx, dir)
	if err != nil {
		return "", err
	}
	if entryName == "" {
		entryName = entryFileNames[0]
	}
	return join(dir, entryName), nil
}

// splitAddr splits "{name}" or "{name}/rest" into its parts.
func splitAddr(addr string) (name, rest string) {
	addr = strings.Trim(strings.TrimSpace(addr), "/")
	if addr == "" || addr == "." {
		return "", ""
	}
	if i := strings.Index(addr, "/"); i >= 0 {
		return addr[:i], strings.Trim(addr[i+1:], "/")
	}
	return addr, ""
}

// join joins path segments with "/", collapsing empties.
func join(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" && p != "." {
			kept = append(kept, strings.Trim(p, "/"))
		}
	}
	if len(kept) == 0 {
		return "."
	}
	return strings.Join(kept, "/")
}

// ── frontmatter ──

// parseFrontmatter extracts YAML frontmatter and body from markdown content.
// Frontmatter is delimited by --- lines at the top of the file.
// The frontmatter is unmarshalled into a program.Meta struct, which includes
// the Locked field that controls whether meta-extensions can modify the
// program.
func parseFrontmatter(raw string) (program.Meta, string, error) {
	var meta program.Meta

	// Check for YAML frontmatter delimited by ---.
	content := strings.TrimSpace(raw)
	if !strings.HasPrefix(content, "---") {
		return meta, raw, nil // no frontmatter
	}

	// Find the closing ---.
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		// Check if --- is at the very start of rest.
		if strings.HasPrefix(rest, "---") {
			rest = rest[3:]
			idx = strings.Index(rest, "\n---")
		}
	}
	if idx == -1 {
		return meta, raw, nil // malformed, treat as body
	}

	yamlBlock := strings.TrimSpace(rest[:idx])
	body := strings.TrimSpace(rest[idx+4:]) // skip "\n---"

	if err := yaml.Unmarshal([]byte(yamlBlock), &meta); err != nil {
		return meta, raw, fmt.Errorf("yaml parse: %w", err)
	}

	return meta, body, nil
}

// renderEntry serialises a Program's metadata and body the way this backend
// stores them: YAML frontmatter, then the content.
func renderEntry(meta program.Meta, body string) (string, error) {
	var sb strings.Builder
	sb.WriteString("---\n")
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(meta); err != nil {
		return "", fmt.Errorf("encode frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("flush frontmatter: %w", err)
	}
	sb.WriteString("---\n\n")
	sb.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// formTypeFromPath returns the FormType based on file extension.
func formTypeFromPath(p string) program.FormType {
	ext := strings.ToLower(path.Ext(p))
	switch ext {
	case ".niq":
		return program.FormTypeScript
	default:
		return program.FormTypePrompt
	}
}
