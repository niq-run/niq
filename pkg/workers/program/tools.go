package program

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/program"
	"github.com/niq-run/niq/pkg/baseworker"
)

// The program worker's tools, each its own event type.
const (
	TypeSearch   event.EventType = "search"
	TypeLoad     event.EventType = "load"
	TypeEdit     event.EventType = "edit"
	TypeRegister event.EventType = "register"
	TypeDelete   event.EventType = "delete"
)

// registerExtensions declares the program tools: each is an extension served
// by its own event type, announced to peers via AnnounceReady.
func (w *Worker) registerExtensions() {
	ctx := context.Background()

	w.Register(baseworker.Extension{
		Event:       TypeSearch,
		Description: "Search for available programs by name, description, or tag. Returns matching programs with their metadata (name, content_type, description, tags, locked). Use load to read the actual content.",
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
		tc.Name = string(evt.Type)
		w.handleSearch(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeLoad,
		Description: "Load a specific content file from a program. Use this to progressively load sub-contents after reading the entry content via the search tool.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"program": map[string]any{
					"type":        "string",
					"description": "Program name.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to the content file within the program directory (e.g. 'rules/go.md').",
				},
			},
			"required": []any{"program", "path"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		tc.Name = string(evt.Type)
		w.handleLoad(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeEdit,
		Description: "Edit a program's content file with an atomic find-and-replace. Cannot edit locked programs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"program": map[string]any{
					"type":        "string",
					"description": "Program name (also the directory name).",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to the content file within the program directory (e.g. 'PROGRAM.md' or 'rules/go.md').",
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
			"required": []any{"program", "path", "old_text"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		tc.Name = string(evt.Type)
		w.handleEdit(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeRegister,
		Description: "Register a new program at runtime. Creates the program directory and PROGRAM.md file on disk. Cannot modify or overwrite locked programs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Program name (also used as the directory name).",
				},
				"content_type": map[string]any{
					"type":        "string",
					"description": "Program type: 'instruction' or 'playbook'.",
					"enum":        []string{"instruction", "playbook"},
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Short description of what this program does.",
				},
				"tags": map[string]any{
					"type":        "array",
					"description": "Tags for search and categorization.",
					"items":       map[string]any{"type": "string"},
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The entry content body (the part after the YAML frontmatter).",
				},
			},
			"required": []any{"name", "content_type", "content"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		tc.Name = string(evt.Type)
		w.handleRegister(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeDelete,
		Description: "Delete a program and all its contents. Cannot delete locked programs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Program name to delete.",
				},
			},
			"required": []any{"name"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		tc.Name = string(evt.Type)
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

	progs, err := w.search(query, ct)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("search error: %v", err), tc.TraceID)
		return
	}

	// Build a readable result: name, content_type, description, tags, locked.
	type resultItem struct {
		Name        string   `json:"name"`
		ContentType string   `json:"content_type"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Locked      bool     `json:"locked"`
	}
	results := make([]resultItem, len(progs))
	for i, p := range progs {
		results[i] = resultItem{
			Name:        p.Name,
			ContentType: string(p.ContentType),
			Description: p.Description,
			Tags:        p.Tags,
			Locked:      p.Locked,
		}
	}

	b, err := json.Marshal(results)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("marshal error: %v", err), tc.TraceID)
		return
	}

	w.ReplyCompleted(tc.CallerID, tc.CallID, tc.Name, string(b), tc.TraceID)
	log.Printf("[program] search query=%q ct=%q → %d results", query, ctStr, len(results))
}

// handleLoad handles the load tool call.
func (w *Worker) handleLoad(ctx context.Context, tc baseworker.ToolCall) {
	progName, _ := tc.Args["program"].(string)
	contentPath, _ := tc.Args["path"].(string)

	if progName == "" || contentPath == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, "program and path are required", tc.TraceID)
		return
	}

	// Read from the backend.
	fullPath := joinPath(progName, contentPath)
	raw, err := w.backend.Read(ctx, fullPath)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("read %s: %v", fullPath, err), tc.TraceID)
		return
	}

	// For markdown files, strip frontmatter if present.
	_, body, _ := parseFrontmatter(raw)
	if body != "" {
		raw = body
	}

	w.ReplyCompleted(tc.CallerID, tc.CallID, tc.Name, raw, tc.TraceID)
	log.Printf("[program] load %s/%s → backend (%d chars)", progName, contentPath, len(raw))
}

// handleEdit handles the edit tool call.
// Locked programs cannot be edited via this tool.
func (w *Worker) handleEdit(ctx context.Context, tc baseworker.ToolCall) {
	progName, _ := tc.Args["program"].(string)
	contentPath, _ := tc.Args["path"].(string)
	oldText, _ := tc.Args["old_text"].(string)
	newText, _ := tc.Args["new_text"].(string)

	if progName == "" || contentPath == "" || oldText == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, "program, path, and old_text are required", tc.TraceID)
		return
	}

	// Locked programs cannot be modified via meta-extensions.
	if existing, err := w.get(progName); err == nil && existing.Locked {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name,
			fmt.Sprintf("cannot edit locked program: %q", progName), tc.TraceID)
		return
	}

	fullPath := joinPath(progName, contentPath)
	if err := w.backend.Edit(ctx, fullPath, oldText, newText); err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("edit %s: %v", fullPath, err), tc.TraceID)
		return
	}

	w.ReplyCompleted(tc.CallerID, tc.CallID, tc.Name,
		fmt.Sprintf("edited %s in program %q", contentPath, progName), tc.TraceID)
	log.Printf("[program] edit %s/%s", progName, contentPath)
}

// handleRegister handles the register tool call.
// Programs registered via this tool are always created as unlocked.
// The Locked flag can only be set through the backend (writing to disk directly)
// — meta-extensions cannot create locked programs.
func (w *Worker) handleRegister(ctx context.Context, tc baseworker.ToolCall) {
	name, _ := tc.Args["name"].(string)
	ctStr, _ := tc.Args["content_type"].(string)
	desc, _ := tc.Args["description"].(string)
	content, _ := tc.Args["content"].(string)

	// Parse tags from args.
	var tags []string
	if tagsRaw, ok := tc.Args["tags"].([]any); ok {
		for _, t := range tagsRaw {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	if name == "" || ctStr == "" || content == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, "name, content_type, and content are required", tc.TraceID)
		return
	}

	// Check if the program already exists and is locked.
	if existing, err := w.get(name); err == nil && existing.Locked {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name,
			fmt.Sprintf("cannot modify locked program: %q", name), tc.TraceID)
		return
	}

	var ct program.ContentType
	switch ctStr {
	case "instruction":
		ct = program.ContentTypeInstruction
	case "playbook":
		ct = program.ContentTypePlaybook
	default:
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("invalid content_type: %s", ctStr), tc.TraceID)
		return
	}

	// Programs registered via tool are always unlocked.
	// Locked programs can only be created by writing to the backend directly.
	fullContent := fmt.Sprintf("---\nname: %s\ncontent_type: %s\ndescription: %s\ntags: [%s]\n---\n\n%s",
		name, ctStr, desc, strings.Join(tags, ", "), content)

	entryPath := joinPath(name, "PROGRAM.md")
	if err := w.backend.Write(ctx, entryPath, fullContent); err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("write failed: %v", err), tc.TraceID)
		return
	}

	prog := &program.Program{
		Meta: program.Meta{
			Name:        name,
			ContentType: ct,
			Description: desc,
			Tags:        tags,
			Locked:      false, // always unlocked when created via tool
		},
	}

	if err := w.register(prog); err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("register failed: %v", err), tc.TraceID)
		return
	}

	w.ReplyCompleted(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("program %q registered", name), tc.TraceID)
	log.Printf("[program] register: %s (%s)", name, ct)
}

// handleDelete handles the delete tool call.
// Locked programs cannot be deleted via this tool.
func (w *Worker) handleDelete(ctx context.Context, tc baseworker.ToolCall) {
	name, _ := tc.Args["name"].(string)

	if name == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, "name is required", tc.TraceID)
		return
	}

	// Check if the program exists and is locked.
	existing, err := w.get(name)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("program %q not found", name), tc.TraceID)
		return
	}
	if existing.Locked {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name,
			fmt.Sprintf("cannot delete locked program: %q", name), tc.TraceID)
		return
	}

	// Remove the program directory from the backend.
	// We use the program name as the directory path.
	if err := w.backend.Remove(ctx, name); err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("delete failed: %v", err), tc.TraceID)
		return
	}

	// Remove from the in-memory cache.
	w.pMu.Lock()
	delete(w.programs, name)
	w.pMu.Unlock()

	w.ReplyCompleted(tc.CallerID, tc.CallID, tc.Name, fmt.Sprintf("program %q deleted", name), tc.TraceID)
	log.Printf("[program] delete: %s", name)
}
