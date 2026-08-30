// Package pgbackend provides a filesystem-based implementation of
// program.Backend using wsbackend.EmbeddedBackend.
package pgbackend

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/niq-run/niq/core/program"
	"github.com/niq-run/niq/ext/service/wsbackend"
)

// Backend adapts wsbackend.EmbeddedBackend to program.Backend.
// It bridges the generic workspace file operations to the Program domain:
//   - Read always returns full content (wsbackend supports offset/limit for
//     workspace editing, but ProgramContent is always loaded whole).
//   - DirEntry paths are normalised to forward slashes.
type Backend struct {
	be *wsbackend.EmbeddedBackend
}

// New returns a new Backend rooted at dir.
func New(dir string) *Backend {
	return &Backend{be: wsbackend.NewEmbeddedBackend(dir)}
}

// Read loads the full text of a ProgramContent by its path.
func (b *Backend) Read(ctx context.Context, path string) (string, error) {
	return b.be.Read(ctx, path, 0, 0)
}

// Write persists a ProgramContent at the given path.
func (b *Backend) Write(ctx context.Context, path, content string) error {
	return b.be.Write(ctx, path, content)
}

// Edit performs an atomic find-and-replace within a ProgramContent.
func (b *Backend) Edit(ctx context.Context, path, oldStr, newStr string) error {
	return b.be.Edit(ctx, path, oldStr, newStr)
}

// List discovers the child entries within a Program directory.
func (b *Backend) List(ctx context.Context, path string) ([]program.DirEntry, error) {
	entries, err := b.be.List(ctx, path)
	if err != nil {
		return nil, err
	}
	result := make([]program.DirEntry, len(entries))
	for i, e := range entries {
		result[i] = program.DirEntry{
			Name:  strings.ReplaceAll(e.Name, string(filepath.Separator), "/"),
			IsDir: e.IsDir,
		}
	}
	return result, nil
}

// Remove deletes a program directory and all its contents from the backend.
func (b *Backend) Remove(ctx context.Context, path string) error {
	return b.be.Remove(ctx, path)
}
