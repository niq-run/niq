package program

import "context"

// Backend is the storage interface for Program content.
// Implementations may use the local filesystem, a database, an object store,
// or any other storage medium. The caller is agnostic to the storage backend.
//
// A Backend is the sole interpreter of Program addresses. Every path argument
// below is an abstract address in the form "{program name}" for a Program's
// entry content, or "{program name}/path/to/file.type" for one of its
// contents. How an address maps onto actual storage is the Backend's own
// business and is never exposed: a filesystem Backend might keep a Program in
// a nested directory far from the root, and no caller can tell.
//
// The same applies to storage format. The bundled filesystem implementation
// (pgbackend) stores each content as YAML frontmatter plus a markdown body and
// reads the body back; another Backend could store metadata in columns. Callers
// see content, not serialisation.
type Backend interface {
	// List enumerates every Program the Backend holds. Each carries its
	// name, its root path, and the paths of its contents — all abstract
	// addresses. There is no notion of a directory here; a Backend whose
	// storage is hierarchical flattens it.
	List(ctx context.Context) ([]*Program, error)

	// Read loads the content at path. A Program's root path yields its entry
	// content; any other path yields that file.
	Read(ctx context.Context, path string) (string, error)

	// Upsert persists a Program's metadata, creating it or replacing an
	// existing unlocked one. It never touches content: a Program created
	// this way starts with an empty entry body, and updating metadata leaves
	// the current body alone. Metadata and content are written by different
	// calls on purpose — content belongs to Write and Edit.
	Upsert(ctx context.Context, p *Program) error

	// Write replaces the content at path outright. At a Program's root path
	// it replaces the entry body and leaves the metadata alone; at any
	// deeper path it writes that file. The Program must already exist.
	Write(ctx context.Context, path, content string) error

	// Edit performs an atomic find-and-replace within the content at path.
	Edit(ctx context.Context, path, oldStr, newStr string) error

	// Remove deletes the Program at a root path, or a single content file at
	// any deeper path.
	Remove(ctx context.Context, path string) error
}
