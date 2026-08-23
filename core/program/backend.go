package program

import "context"

// DirEntry represents a child entry within a Program directory.
type DirEntry struct {
	Name  string
	IsDir bool
}

// Backend is the storage interface for Program content.
// Implementations may use the local filesystem, a database, an object store,
// or any other storage medium. The caller is agnostic to the storage backend.
//
// All paths are relative to the program root directory, e.g.
// "code-review/PROGRAM.md" or "code-review/rules/go.md".
type Backend interface {
	// Read loads the full text of a ProgramContent by its path.
	Read(ctx context.Context, path string) (string, error)

	// Write persists a ProgramContent at the given path, creating any
	// intermediate directories as needed.
	Write(ctx context.Context, path, content string) error

	// Edit performs an atomic find-and-replace within a ProgramContent.
	Edit(ctx context.Context, path, oldStr, newStr string) error

	// Remove deletes a program directory and all its contents.
	// Used by the delete tool to remove a program from the backend.
	Remove(ctx context.Context, path string) error

	// List discovers the child entries within a Program directory.
	// Used during program discovery to find PROGRAM.md / SKILL.md entry
	// files and enumerate sub-contents.
	List(ctx context.Context, path string) ([]DirEntry, error)
}
