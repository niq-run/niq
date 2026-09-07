package program

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/niq-run/niq/core/event"
	coreprogram "github.com/niq-run/niq/core/program"
	"github.com/niq-run/niq/pkg/baseworker"
	"github.com/niq-run/niq/pkg/services/pgbackend"
)

// writeProgram creates a program directory at relDir under root, with a
// PROGRAM.md carrying the given frontmatter name and description.
// contentType is written verbatim as content_type.
func writeProgram(t *testing.T, root, relDir, name, desc, body string) {
	t.Helper()
	writeProgramAs(t, root, relDir, name, "playbook", desc, body)
}

func writeProgramAs(t *testing.T, root, relDir, name, contentType, desc, body string) {
	t.Helper()
	dir := filepath.Join(root, relDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := "---\nname: " + name +
		"\ncontent_type: " + contentType +
		"\ndescription: " + desc +
		"\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "PROGRAM.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relDir, err)
	}
}

// writeContent creates a sub-content file inside a program directory.
func writeContent(t *testing.T, root, relDir, relFile, body string) {
	t.Helper()
	p := filepath.Join(root, relDir, relFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", relDir, err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", relFile, err)
	}
}

func newTestWorker(root string) *Worker {
	return New(Config{ID: "program", Backend: pgbackend.New(root)})
}

// listByName is a small helper for tests that just want one program.
func listByName(t *testing.T, w *Worker, name string) *coreprogram.Program {
	t.Helper()
	progs, err := w.backend.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range progs {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("program %q not found in %d programs", name, len(progs))
	return nil
}

// TestProgramIsAddressedByNameNotDirectory is the core of the addressing
// model: a Program may live at any depth, but callers only ever see its name
// as the root path. Nothing outside the backend knows about "vendor/deep".
func TestProgramIsAddressedByNameNotDirectory(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "vendor/deep", "foo", "a nested program", "ENTRY BODY")

	w := newTestWorker(root)
	p := listByName(t, w, "foo")

	if p.Path != "foo" {
		t.Fatalf("Path = %q, want %q", p.Path, "foo")
	}
	if p.Name != "foo" {
		t.Fatalf("Name = %q, want %q", p.Name, "foo")
	}
	if p.EntryContent.Path != "foo" {
		t.Fatalf("EntryContent.Path = %q, want %q", p.EntryContent.Path, "foo")
	}

	// The entry content is reachable by the program's name alone.
	got, err := w.backend.Read(context.Background(), "foo")
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if got != "ENTRY BODY" {
		t.Fatalf("entry content = %q, want %q", got, "ENTRY BODY")
	}
}

// TestContentPathsHangOffProgramName checks sub-contents are addressed as
// "{name}/path", wherever the directory actually is.
func TestContentPathsHangOffProgramName(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "vendor/deep", "foo", "nested", "ENTRY")
	writeContent(t, root, "vendor/deep", "rules/go.md", "RULE BODY")

	w := newTestWorker(root)
	p := listByName(t, w, "foo")

	var paths []string
	for _, c := range p.Contents {
		if c.Name != "foo" {
			t.Fatalf("content Name = %q, want %q", c.Name, "foo")
		}
		paths = append(paths, c.Path)
	}
	if len(paths) != 1 || paths[0] != "foo/rules/go.md" {
		t.Fatalf("content paths = %v, want [foo/rules/go.md]", paths)
	}

	got, err := w.backend.Read(context.Background(), "foo/rules/go.md")
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if got != "RULE BODY" {
		t.Fatalf("content = %q, want %q", got, "RULE BODY")
	}
}

// TestReadStripsFrontmatter pins that storage format stays in the backend:
// callers get content, never serialisation.
func TestReadStripsFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "bar", "bar", "a program", "BODY ONLY")

	w := newTestWorker(root)
	got, err := w.backend.Read(context.Background(), "bar")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "BODY ONLY" {
		t.Fatalf("read = %q, want %q", got, "BODY ONLY")
	}
	if strings.Contains(got, "---") || strings.Contains(got, "content_type") {
		t.Fatalf("read leaked frontmatter: %q", got)
	}
}

// TestContentTypeIsReadFromDisk is the regression test for instruction
// programs silently coming back as playbook.
func TestContentTypeIsReadFromDisk(t *testing.T) {
	root := t.TempDir()
	writeProgramAs(t, root, "rules", "rules", "instruction", "a binding rule", "BODY")

	w := newTestWorker(root)
	p := listByName(t, w, "rules")
	if p.ContentType != coreprogram.ContentTypeInstruction {
		t.Fatalf("ContentType = %q, want %q", p.ContentType, coreprogram.ContentTypeInstruction)
	}
}

// TestListSeesProgramsAddedAtRuntime covers the no-cache rule: programs
// dropped in after construction are found without a restart.
func TestListSeesProgramsAddedAtRuntime(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "bar", "bar", "first", "one")

	w := newTestWorker(root)
	ctx := context.Background()

	before, err := w.search(ctx, "", "")
	if err != nil || len(before) != 1 {
		t.Fatalf("before = %d programs (err %v), want 1", len(before), err)
	}

	writeProgram(t, root, "vendor/baz", "baz", "second", "two")

	after, err := w.search(ctx, "", "")
	if err != nil || len(after) != 2 {
		t.Fatalf("after = %d programs (err %v), want 2", len(after), err)
	}
}

// TestListSeesEditsAtRuntime covers the other half of staleness: metadata
// edited on disk is picked up too.
func TestListSeesEditsAtRuntime(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "bar", "bar", "original", "body")

	w := newTestWorker(root)
	ctx := context.Background()

	if got, _ := w.search(ctx, "original", ""); len(got) != 1 {
		t.Fatalf("search original = %d, want 1", len(got))
	}

	edited := "---\nname: bar\ncontent_type: playbook\ndescription: changed\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(root, "bar", "PROGRAM.md"), []byte(edited), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	if got, _ := w.search(ctx, "original", ""); len(got) != 0 {
		t.Fatalf("search original after edit = %d, want 0", len(got))
	}
	if got, _ := w.search(ctx, "changed", ""); len(got) != 1 {
		t.Fatalf("search changed = %d, want 1", len(got))
	}
}

// TestSearchFiltersByContentType checks the optional content_type filter.
func TestSearchFiltersByContentType(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "p", "p", "a playbook", "b")

	w := newTestWorker(root)
	ctx := context.Background()

	if got, err := w.search(ctx, "", coreprogram.ContentTypePlaybook); err != nil || len(got) != 1 {
		t.Fatalf("playbook = %d (err %v), want 1", len(got), err)
	}
	if got, err := w.search(ctx, "", coreprogram.ContentTypeInstruction); err != nil || len(got) != 0 {
		t.Fatalf("instruction = %d (err %v), want 0", len(got), err)
	}
}

// TestSymlinkInsideMountIsFollowed covers symlinked directories: os.ReadDir
// reports a symlink as a non-directory, so without seeing through the link a
// symlinked program directory is never discovered.
func TestSymlinkInsideMountIsFollowed(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "real/inner", "inner", "behind a symlink", "BODY")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	w := newTestWorker(root)
	if _, err := w.backend.Read(context.Background(), "inner"); err != nil {
		t.Fatalf("read symlinked program: %v", err)
	}
}

// TestSymlinkOutsideMountIsFollowed pins the relaxed containment rule: a
// symlink pointing outside the mount resolves beyond it, but the logical path
// is still inside, so the backend follows it.
func TestSymlinkOutsideMountIsFollowed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeProgram(t, outside, "ext", "ext", "outside the mount", "BODY")
	if err := os.Symlink(filepath.Join(outside, "ext"), filepath.Join(root, "ext")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	w := newTestWorker(root)
	got, err := w.backend.Read(context.Background(), "ext")
	if err != nil {
		t.Fatalf("read symlinked-in program: %v", err)
	}
	if got != "BODY" {
		t.Fatalf("content = %q, want %q", got, "BODY")
	}
}

// TestDuplicateNamesDoNotBreak pins that two directories answering to the
// same name still produce a usable listing rather than an error.
func TestDuplicateNamesDoNotBreak(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "aaa/foo", "foo", "first foo", "A")
	writeProgram(t, root, "zzz/foo", "foo", "second foo", "Z")

	w := newTestWorker(root)
	progs, err := w.backend.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(progs) != 1 {
		t.Fatalf("list = %d programs, want 1 (duplicates collapse)", len(progs))
	}
	if _, err := w.backend.Read(context.Background(), "foo"); err != nil {
		t.Fatalf("read: %v", err)
	}
}

// TestUpsertThenWrite covers the split of responsibilities: upsert writes
// metadata and never touches content; write replaces content and never
// touches metadata.
func TestUpsertThenWrite(t *testing.T) {
	root := t.TempDir()
	w := newTestWorker(root)
	ctx := context.Background()

	p := &coreprogram.Program{
		Meta: coreprogram.Meta{
			Name:        "fresh",
			ContentType: coreprogram.ContentTypeInstruction,
			Description: "made at runtime",
			Tags:        []string{"x", "y"},
		},
		Path: "fresh",
	}
	if err := w.backend.Upsert(ctx, p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A fresh program has an empty body.
	if body, err := w.backend.Read(ctx, "fresh"); err != nil || body != "" {
		t.Fatalf("read = %q (err %v), want empty", body, err)
	}

	if err := w.backend.Write(ctx, "fresh", "NEW BODY"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if body, err := w.backend.Read(ctx, "fresh"); err != nil || body != "NEW BODY" {
		t.Fatalf("read = %q (err %v), want NEW BODY", body, err)
	}

	// Updating metadata must leave the body alone.
	p = listByName(t, w, "fresh")
	p.Description = "updated description"
	p.Tags = []string{"z"}
	if err := w.backend.Upsert(ctx, p); err != nil {
		t.Fatalf("upsert update: %v", err)
	}

	updated := listByName(t, w, "fresh")
	if updated.Description != "updated description" {
		t.Fatalf("Description = %q, want updated description", updated.Description)
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != "z" {
		t.Fatalf("Tags = %v, want [z]", updated.Tags)
	}
	if body, err := w.backend.Read(ctx, "fresh"); err != nil || body != "NEW BODY" {
		t.Fatalf("read after meta update = %q (err %v), want NEW BODY", body, err)
	}
}

// TestReadUsesIndexWithoutPriorList pins that Read resolves through the
// name → directory index rather than requiring a fresh walk, and that a
// Program created after the index was built is still found.
func TestReadUsesIndexWithoutPriorList(t *testing.T) {
	root := t.TempDir()
	w := newTestWorker(root)
	ctx := context.Background()

	// Index is empty at this point; Read must still resolve.
	writeProgram(t, root, "vendor/deep", "late", "added later", "LATE BODY")
	if got, err := w.backend.Read(ctx, "late"); err != nil || got != "LATE BODY" {
		t.Fatalf("read = %q (err %v), want LATE BODY", got, err)
	}

	// A second Read hits the index; still correct.
	if got, err := w.backend.Read(ctx, "late"); err != nil || got != "LATE BODY" {
		t.Fatalf("second read = %q (err %v), want LATE BODY", got, err)
	}
}

// ── tool-level tests ──

// stubChannel records what a worker replies with. Tests assert on stored
// state, so the replies mostly just need somewhere to go.
type stubChannel struct {
	mu   sync.Mutex
	sent []event.Event
}

func (c *stubChannel) ID() string { return "stub" }

func (c *stubChannel) Send(_ context.Context, evt event.Event, _ ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, evt)
	return nil
}

func (c *stubChannel) Broadcast(ctx context.Context, evt event.Event) error {
	return c.Send(ctx, evt)
}

func (c *stubChannel) Receive(context.Context) (<-chan event.Event, error) {
	return make(chan event.Event), nil
}

func (c *stubChannel) Close() error { return nil }

func newToolWorker(root string) *Worker {
	return New(Config{ID: "program", Bus: &stubChannel{}, Backend: pgbackend.New(root)})
}

func toolCall(args map[string]any) baseworker.ToolCall {
	return baseworker.ToolCall{CallerID: "tester", CallID: "c1", Args: args}
}

// TestHandleUpsertCreatesThenWrite checks the tool flow: upsert makes the
// program with its metadata, write fills in the content.
func TestHandleUpsertCreatesThenWrite(t *testing.T) {
	root := t.TempDir()
	w := newToolWorker(root)
	ctx := context.Background()

	w.handleUpsert(ctx, toolCall(map[string]any{
		"name":         "p",
		"content_type": "instruction",
		"description":  "a rule",
		"tags":         []any{"x", "y"},
	}))

	got := listByName(t, w, "p")
	if got.ContentType != coreprogram.ContentTypeInstruction {
		t.Fatalf("ContentType = %q, want instruction", got.ContentType)
	}
	if got.Description != "a rule" || len(got.Tags) != 2 {
		t.Fatalf("Description = %q, Tags = %v", got.Description, got.Tags)
	}

	w.handleWrite(ctx, toolCall(map[string]any{
		"path":    "p",
		"content": "BODY",
	}))
	if body, _ := w.backend.Read(ctx, "p"); body != "BODY" {
		t.Fatalf("body = %q, want BODY", body)
	}

	// Writing to an unknown program is refused — creation is upsert's job.
	w.handleWrite(ctx, toolCall(map[string]any{
		"path":    "ghost",
		"content": "nope",
	}))
	if _, err := w.backend.Read(ctx, "ghost"); err == nil {
		t.Fatal("write to unknown program unexpectedly created it")
	}
}

// TestHandleUpsertUpdatesOnlySuppliedFields is the point of upsert: changing
// one field must leave the rest — and the body — alone.
func TestHandleUpsertUpdatesOnlySuppliedFields(t *testing.T) {
	root := t.TempDir()
	w := newToolWorker(root)
	ctx := context.Background()

	w.handleUpsert(ctx, toolCall(map[string]any{
		"name":         "p",
		"content_type": "instruction",
		"description":  "a rule",
		"tags":         []any{"x", "y"},
	}))
	w.handleWrite(ctx, toolCall(map[string]any{"path": "p", "content": "BODY"}))

	// Only the description changes; content is not part of upsert at all.
	w.handleUpsert(ctx, toolCall(map[string]any{
		"name":        "p",
		"description": "a better rule",
	}))

	got := listByName(t, w, "p")
	if got.Description != "a better rule" {
		t.Fatalf("Description = %q, want a better rule", got.Description)
	}
	if got.ContentType != coreprogram.ContentTypeInstruction {
		t.Fatalf("ContentType = %q, want instruction (unchanged)", got.ContentType)
	}
	if len(got.Tags) != 2 {
		t.Fatalf("Tags = %v, want unchanged 2 entries", got.Tags)
	}
	if body, _ := w.backend.Read(ctx, "p"); body != "BODY" {
		t.Fatalf("body = %q, want BODY (unchanged)", body)
	}
}

// TestHandleUpsertRejectsLockedPrograms covers the guard.
func TestHandleUpsertRejectsLockedPrograms(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "locked", "locked", "a locked program", "BODY")
	// Mark it locked on disk.
	raw, err := os.ReadFile(filepath.Join(root, "locked", "PROGRAM.md"))
	if err != nil {
		t.Fatal(err)
	}
	locked := strings.Replace(string(raw), "description:", "locked: true\ndescription:", 1)
	if err := os.WriteFile(filepath.Join(root, "locked", "PROGRAM.md"), []byte(locked), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newToolWorker(root)
	w.handleUpsert(context.Background(), toolCall(map[string]any{
		"name":        "locked",
		"description": "should not stick",
	}))

	if got := listByName(t, w, "locked"); got.Description != "a locked program" {
		t.Fatalf("Description = %q, want unchanged", got.Description)
	}
}

// TestProgramForPathResolvesOwner checks the helper the locked checks rely on.
func TestProgramForPathResolvesOwner(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "vendor/deep", "foo", "nested", "ENTRY")
	writeContent(t, root, "vendor/deep", "rules/go.md", "RULE")

	w := newTestWorker(root)
	ctx := context.Background()

	for _, addr := range []string{"foo", "foo/rules/go.md"} {
		got, err := w.programForPath(ctx, addr)
		if err != nil {
			t.Fatalf("programForPath(%q): %v", addr, err)
		}
		if got.Name != "foo" {
			t.Fatalf("programForPath(%q).Name = %q, want foo", addr, got.Name)
		}
	}
	if _, err := w.programForPath(ctx, "nope/x.md"); err == nil {
		t.Fatal("programForPath on unknown address: want error")
	}
}
