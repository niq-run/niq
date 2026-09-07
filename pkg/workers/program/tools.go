package program

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/program"
	"github.com/niq-run/niq/pkg/baseworker"
)

// The program worker's tools, each its own event type.
const (
	TypeSearch event.EventType = "search"
	TypeLoad   event.EventType = "load"
	TypeWrite  event.EventType = "write"
	TypeEdit   event.EventType = "edit"
	TypeUpsert event.EventType = "upsert"
	TypeDelete event.EventType = "delete"
)

// registerExtensions declares the program tools: each is an extension served
// by its own event type, announced to peers via AnnounceReady.
func (w *Worker) registerExtensions() {
	ctx := context.Background()

	w.Register(baseworker.Extension{
		Event:       TypeSearch,
		Description: "Search for available programs by name, description, or tag. Returns matching programs with their metadata (name, content_type, description, tags, locked), the root path to load their entry content, and the paths of any sub-contents.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query — matches against program name, description, and tags (case-insensitive substring).",
				},
				"content_type": map[string]any{
					"type":        "string",
					"description": "Optional filter: 'instruction' or 'playbook'.",
					"enum":        []string{"instruction", "playbook"},
				},
			},
			"required": []any{"query"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleSearch(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeLoad,
		Description: "Load a program's content by its path. '{program_name}' loads the program's entry content; '{program_name}/path/to/file.md' loads one of its sub-contents.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Content path: '{program_name}' for the entry content, or '{program_name}/rules/go.md' for a sub-content.",
				},
			},
			"required": []any{"path"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleLoad(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeEdit,
		Description: "Edit a program's content with an atomic find-and-replace. Cannot edit locked programs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Content path: '{program_name}' for the entry content, or '{program_name}/rules/go.md' for a sub-content.",
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "The exact text to find and replace.",
				},
				"new_text": map[string]any{
					"type":        "string",
					"description": "The replacement text. May be empty to delete the matched text.",
				},
			},
			"required": []any{"path", "old_text"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleEdit(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeUpsert,
		Description: "Create a program, or update an existing one's metadata (content_type, description, tags). Omit a field to leave it as it is; only 'name' is required when updating. This never touches content — write content with the write tool, change it in place with edit. Cannot modify locked programs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Program name — also its root path, so load(\"<name>\") reads its entry content.",
				},
				"content_type": map[string]any{
					"type":        "string",
					"description": "Program type: 'instruction' or 'playbook'. Required when creating.",
					"enum":        []string{"instruction", "playbook"},
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Short description of what this program does.",
				},
				"tags": map[string]any{
					"type":        "array",
					"description": "Tags for search and categorization. Replaces the existing tags.",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []any{"name"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleUpsert(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeWrite,
		Description: "Replace a program's content outright. '{program_name}' replaces the entry content; '{program_name}/rules/go.md' replaces a sub-content (creating the file if absent). The program must exist — create it with upsert first. For small in-place changes prefer edit. Cannot write to locked programs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Content path: '{program_name}' for the entry content, or '{program_name}/rules/go.md' for a sub-content.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The full replacement content.",
				},
			},
			"required": []any{"path", "content"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleWrite(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeDelete,
		Description: "Delete a program and all its contents, or a single content file. Cannot delete locked programs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "'{program_name}' deletes the whole program; '{program_name}/rules/go.md' deletes one content file.",
				},
			},
			"required": []any{"path"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleDelete(ctx, tc)
	})
}

// handleSearch handles the search tool call.
func (w *Worker) handleSearch(ctx context.Context, tc baseworker.ToolCall) {
	query, _ := tc.Args["query"].(string)
	ctStr, _ := tc.Args["content_type"].(string)

	var ct program.ContentType
	switch ctStr {
	case "instruction":
		ct = program.ContentTypeInstruction
	case "playbook":
		ct = program.ContentTypePlaybook
	}

	progs, err := w.search(ctx, query, ct)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("search error: %v", err), tc.TraceID)
		return
	}

	// Build a readable result: metadata, the root path to load the entry
	// content, and the paths of any sub-contents for progressive loading.
	type resultItem struct {
		Name        string   `json:"name"`
		ContentType string   `json:"content_type"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Locked      bool     `json:"locked"`
		Path        string   `json:"path"`
		Contents    []string `json:"contents,omitempty"`
	}
	results := make([]resultItem, len(progs))
	for i, p := range progs {
		item := resultItem{
			Name:        p.Name,
			ContentType: string(p.ContentType),
			Description: p.Description,
			Tags:        p.Tags,
			Locked:      p.Locked,
			Path:        p.Path,
		}
		for _, c := range p.Contents {
			item.Contents = append(item.Contents, c.Path)
		}
		results[i] = item
	}

	b, err := json.Marshal(results)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("marshal error: %v", err), tc.TraceID)
		return
	}

	w.ReplyCompleted(tc.CallerID, tc.CallID, string(b), tc.TraceID)
	log.Printf("[program] search query=%q ct=%q → %d results", query, ctStr, len(results))
}

// handleLoad handles the load tool call. It takes a single address:
// "{name}" for a program's entry content, "{name}/path" for a sub-content.
func (w *Worker) handleLoad(ctx context.Context, tc baseworker.ToolCall) {
	contentPath, _ := tc.Args["path"].(string)
	if contentPath == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, "path is required", tc.TraceID)
		return
	}

	// The backend resolves the address and owns the storage format, so
	// frontmatter never reaches the caller.
	raw, err := w.backend.Read(ctx, contentPath)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("read %s: %v", contentPath, err), tc.TraceID)
		return
	}

	w.ReplyCompleted(tc.CallerID, tc.CallID, raw, tc.TraceID)
	log.Printf("[program] load %s → %d chars", contentPath, len(raw))
}

// handleEdit handles the edit tool call.
// Locked programs cannot be edited via this tool.
func (w *Worker) handleEdit(ctx context.Context, tc baseworker.ToolCall) {
	contentPath, _ := tc.Args["path"].(string)
	oldText, _ := tc.Args["old_text"].(string)
	newText, _ := tc.Args["new_text"].(string)

	if contentPath == "" || oldText == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, "path and old_text are required", tc.TraceID)
		return
	}

	// Locked programs cannot be modified via meta-extensions.
	prog, err := w.programForPath(ctx, contentPath)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, err.Error(), tc.TraceID)
		return
	}
	if prog.Locked {
		w.ReplyFailed(tc.CallerID, tc.CallID,
			fmt.Sprintf("cannot edit locked program: %q", prog.Name), tc.TraceID)
		return
	}

	if err := w.backend.Edit(ctx, contentPath, oldText, newText); err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("edit %s: %v", contentPath, err), tc.TraceID)
		return
	}

	w.ReplyCompleted(tc.CallerID, tc.CallID, fmt.Sprintf("edited %s", contentPath), tc.TraceID)
	log.Printf("[program] edit %s", contentPath)
}

// handleUpsert handles the upsert tool call: it creates a Program, or updates
// an existing one's metadata. Only the fields actually supplied are changed —
// an omitted one keeps its current value. Content is deliberately out of
// scope: it is written with the write tool and changed in place with edit.
//
// Programs written through this tool are always unlocked. The Locked flag can
// only be set by writing to the backend directly — meta-extensions cannot
// create locked programs.
func (w *Worker) handleUpsert(ctx context.Context, tc baseworker.ToolCall) {
	name, _ := tc.Args["name"].(string)
	ctStr, _ := tc.Args["content_type"].(string)
	desc, hasDesc := tc.Args["description"].(string)

	var tags []string
	hasTags := false
	if tagsRaw, ok := tc.Args["tags"].([]any); ok {
		hasTags = true
		for _, t := range tagsRaw {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	if name == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, "name is required", tc.TraceID)
		return
	}

	existing, findErr := w.programForPath(ctx, name)
	if findErr == nil && existing.Locked {
		w.ReplyFailed(tc.CallerID, tc.CallID,
			fmt.Sprintf("cannot modify locked program: %q", name), tc.TraceID)
		return
	}
	creating := findErr != nil

	if ctStr == "" && creating {
		w.ReplyFailed(tc.CallerID, tc.CallID, "content_type is required to create a program", tc.TraceID)
		return
	}

	// Start from what is already stored so an update touches only the fields
	// that were supplied.
	p := &program.Program{Path: name}
	p.Name = name
	if !creating {
		p.Meta = existing.Meta
	}

	switch ctStr {
	case "instruction":
		p.ContentType = program.ContentTypeInstruction
	case "playbook":
		p.ContentType = program.ContentTypePlaybook
	case "": // leave as is
	default:
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("invalid content_type: %s", ctStr), tc.TraceID)
		return
	}
	if hasDesc {
		p.Description = desc
	}
	if hasTags {
		p.Tags = tags
	}

	// The backend stores the metadata however it likes; the worker knows
	// nothing about frontmatter or file layout.
	if err := w.backend.Upsert(ctx, p); err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("upsert failed: %v", err), tc.TraceID)
		return
	}

	verb := "updated"
	if creating {
		verb = "created"
	}
	w.ReplyCompleted(tc.CallerID, tc.CallID,
		fmt.Sprintf("program %q %s at %q", name, verb, name), tc.TraceID)
	log.Printf("[program] upsert: %s (%s, %s)", name, p.ContentType, verb)
}

// handleWrite handles the write tool call: it replaces the content at a path
// outright. Metadata is out of scope — that belongs to upsert.
func (w *Worker) handleWrite(ctx context.Context, tc baseworker.ToolCall) {
	contentPath, _ := tc.Args["path"].(string)
	content, _ := tc.Args["content"].(string)

	if contentPath == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, "path is required", tc.TraceID)
		return
	}

	// The program must already exist — creating one is upsert's job, since
	// it needs metadata.
	prog, err := w.programForPath(ctx, contentPath)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID,
			fmt.Sprintf("program %q not found — create it with upsert first", contentPath), tc.TraceID)
		return
	}
	if prog.Locked {
		w.ReplyFailed(tc.CallerID, tc.CallID,
			fmt.Sprintf("cannot write to locked program: %q", prog.Name), tc.TraceID)
		return
	}

	if err := w.backend.Write(ctx, contentPath, content); err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("write %s: %v", contentPath, err), tc.TraceID)
		return
	}

	w.ReplyCompleted(tc.CallerID, tc.CallID, fmt.Sprintf("wrote %s", contentPath), tc.TraceID)
	log.Printf("[program] write %s (%d chars)", contentPath, len(content))
}

// handleDelete handles the delete tool call.
// Locked programs cannot be deleted via this tool.
func (w *Worker) handleDelete(ctx context.Context, tc baseworker.ToolCall) {
	contentPath, _ := tc.Args["path"].(string)

	if contentPath == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, "path is required", tc.TraceID)
		return
	}

	// Resolve the owning program, and refuse locked ones.
	prog, err := w.programForPath(ctx, contentPath)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("program %q not found", contentPath), tc.TraceID)
		return
	}
	if prog.Locked {
		w.ReplyFailed(tc.CallerID, tc.CallID,
			fmt.Sprintf("cannot delete locked program: %q", prog.Name), tc.TraceID)
		return
	}

	if err := w.backend.Remove(ctx, contentPath); err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("delete failed: %v", err), tc.TraceID)
		return
	}

	// Nothing to evict: the backend is the only source of truth.
	w.ReplyCompleted(tc.CallerID, tc.CallID, fmt.Sprintf("deleted %s", contentPath), tc.TraceID)
	log.Printf("[program] delete: %s", contentPath)
}
