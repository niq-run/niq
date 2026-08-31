package program

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"

	"github.com/niq-run/niq/core/program"
	"gopkg.in/yaml.v3"
)

// entryFileNames is the list of recognized entry file names for Program discovery.
var entryFileNames = []string{"PROGRAM.md", "SKILL.md"}

// discover scans the program root directory recursively, finds all
// PROGRAM.md / SKILL.md files, parses them, and registers each Program.
func (w *Worker) discover(ctx context.Context) error {
	return w.walkDir(ctx, ".")
}

// walkDir recursively discovers programs starting from dir (relative to root).
func (w *Worker) walkDir(ctx context.Context, dir string) error {
	entries, err := w.backend.List(ctx, dir)
	if err != nil {
		return fmt.Errorf("list %q: %w", dir, err)
	}

	for _, e := range entries {
		relPath := joinPath(dir, e.Name)
		if e.IsDir {
			// Check if this directory contains an entry file.
			prog, err := w.tryLoadProgram(ctx, relPath)
			if err != nil {
				log.Printf("[program] skip %s: %v", relPath, err)
			}
			if prog != nil {
				if err := w.register(prog); err != nil {
					log.Printf("[program] register %s: %v", prog.Name, err)
				} else {
					log.Printf("[program] discovered: %s (%s)", prog.Name, prog.ContentType)
				}
				// Don't recurse into program directories — programs are
				// self-contained; their sub-contents are loaded on demand.
				continue
			}
			// Not a program directory — recurse into it.
			if err := w.walkDir(ctx, relPath); err != nil {
				log.Printf("[program] walk %s: %v", relPath, err)
			}
		}
	}
	return nil
}

// tryLoadProgram attempts to load a program from a directory. Returns nil
// if the directory does not contain a recognized entry file.
// Only the YAML frontmatter metadata is parsed and kept in memory; the
// entry content body and sub-contents are loaded on demand via program.load.
func (w *Worker) tryLoadProgram(ctx context.Context, dir string) (*program.Program, error) {
	subEntries, err := w.backend.List(ctx, dir)
	if err != nil {
		return nil, err
	}

	// Find the entry file.
	var entryName string
	for _, e := range subEntries {
		if e.IsDir {
			continue
		}
		if slices.Contains(entryFileNames, e.Name) {
			entryName = e.Name
			break
		}
	}
	if entryName == "" {
		return nil, nil // not a program directory
	}

	entryPath := joinPath(dir, entryName)

	// Read the entry file and parse YAML frontmatter for metadata.
	raw, err := w.backend.Read(ctx, entryPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", entryPath, err)
	}

	meta, _, err := parseFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter in %s: %w", entryPath, err)
	}

	// Fallback: use directory name as program name.
	if meta.Name == "" {
		meta.Name = filepath.Base(dir)
	}
	if meta.ContentType == "" {
		meta.ContentType = program.ContentTypePlaybook
	}

	return &program.Program{
		Meta: meta,
	}, nil
}

// parseFrontmatter extracts YAML frontmatter and body from markdown content.
// Frontmatter is delimited by --- lines at the top of the file.
// The frontmatter is unmarshalled into a program.Meta struct, which includes
// the Locked field that controls whether meta-extensions can modify the program.
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

// formTypeFromPath returns the FormType based on file extension.
func formTypeFromPath(path string) program.FormType {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".niq":
		return program.FormTypeScript
	default:
		return program.FormTypePrompt
	}
}

// joinPath joins path segments with "/".
func joinPath(parts ...string) string {
	joined := filepath.Join(parts...)
	// Normalize to forward slashes for consistency across platforms.
	return strings.ReplaceAll(joined, string(filepath.Separator), "/")
}
