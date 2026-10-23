package program

import (
	"context"
	"fmt"
	"log"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/program"
)

// The program worker's tools, each its own event type.
const (
	TypeSearch event.EventType = "search"
	TypeLoad   event.EventType = "load"
	TypeWrite  event.EventType = "write"
	TypeEdit   event.EventType = "edit"
	TypeUpsert event.EventType = "upsert"
	TypeDelete event.EventType = "delete"
	TypeRun    event.EventType = "run"
)

// registerExtensions declares the program tools: each is an extension served
// by its own event type, announced to peers via AnnounceReady.
func (w *Worker) registerExtensions() {
	ctx := context.Background()

	// Self-description: the generic worker.readme read capability, so peers, the
	// WebUI and the worker itself can recall what this worker is for.
	w.RegisterReadme(
		"Read this worker's readme: what the program worker is for (managing and running Programs).",
		w.readmeText, nil)

	w.Register(baseworker.Extension{
		Event: TypeSearch,
		Response: map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":         map[string]any{"type": "string"},
					"content_type": map[string]any{"type": "string", "enum": []string{"instruction", "playbook"}},
					"form_type":    map[string]any{"type": "string", "enum": []string{"prompt", "script"}},
					"description":  map[string]any{"type": "string"},
					"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"locked":       map[string]any{"type": "boolean"},
					"path":         map[string]any{"type": "string", "description": "address to load the entry content"},
					"contents":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
			},
		},
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
		Response:    map[string]any{"type": "string", "description": "the content body (frontmatter stripped)"},
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
		Event: TypeRun,
		Response: baseworker.ObjectSchema(map[string]any{
			"output":        map[string]any{"description": "the script's return value (sdk.result or a top-level return), any JSON type"},
			"log":           baseworker.ArraySchema(map[string]any{"type": "string"}),
			"log_truncated": map[string]any{"type": "number", "description": "how many earlier log lines were dropped"},
		}),
		Description: "Run a fresh, stateless JAVASCRIPT snippet in a separate, short-lived subprocess and return its result. Two modes, exactly one: " +
			"path = run a stored script program (path is its entry content, e.g. \"ptc-report\"; find candidates with program__search, read source with program__load), " +
			"or source = run inline code (ad-hoc, nothing persisted). " +
			"The only API you are given is `sdk`; reach the world through sdk.call. Sandbox contract — the script must not reach the host directly: " +
			"no imports of any kind (`import`, `export`, dynamic `import(...)`, `require`) — there is no module resolution and no package dependencies, only the JavaScript language builtins (JSON, Math, Date, Promise, …); " +
			"no filesystem, process or network APIs (`fs` / `node:fs` / `fs/promises`, `child_process`, `process` env/argv/exit, `fetch` / `XMLHttpRequest` / sockets, `Deno` / `Bun` globals). " +
			"Anything that needs files, a shell, environment variables or the network goes through another worker via sdk.call (e.g. the workspace worker's bash). " +
			"Direct access is outside the contract and unsupported — it is not portable across executors, and an executor may run the script without those globals. " +
			"(1) await sdk.call(target, event, args) sends a normal request to another worker and resolves with its reply VALUE — a string for a prose reply, a real JS object/array for a structured one (no JSON.parse needed; JSON.stringify it if you need text). " +
			"A TOOL-LEVEL failure is an ordinary result, not an exception (e.g. bash exiting 3 resolves to \"Exit code: 3\"), so parse the text instead of wrapping everything in try/catch; " +
			"only a CAPABILITY-level failure rejects (unknown worker, request.failed / rejected). " +
			"Calls run concurrently — `await Promise.all([...])` fans out; keep fan-out modest. " +
			"(2) sdk.result(payload) sets the run output as the value it is (string, object, array …); the LAST call wins. " +
			"A top-level `return payload` does the same and is used only when sdk.result was never called; with neither, the output is an empty string. " +
			"(3) sdk.log(text) is echoed back to the caller AFTER the result under a \"--- script log ---\" header (last 100 lines / 8KB, older lines dropped) — use it for progress and diagnosis. " +
			"(4) sdk.args is the run's args object (never undefined, defaults to {}); a stored script should read its inputs from it. " +
			"Discovering peers: this worker's own tool list need not contain other workers' tools, so look them up with the directory worker (list_workers / get_worker_info) and call them from the script; " +
			"a target the directory does not know fails fast with \"unknown worker: <id>\" (the directory worker owns the roster; the program worker asks it). " +
			"Timing and size: timeout_seconds (default 120) bounds the WHOLE run including every nested call; on expiry the sandbox is killed, in-flight requests are abandoned, and the log collected so far comes back with the error. " +
			"Result text larger than 20KB is truncated to head + tail. " +
			"Reply: an OBJECT {output, log?} — output is the script's return value with its type kept (string, object, array …), log holds the sdk.log lines (tail-capped, with log_truncated when older lines were dropped). " +
			"Diagnostics: a script error is reported with the failing line of YOUR source (e.g. \"at script line 3:7: throw new Error(...)\"); " +
			"use this to execute a whole sequence of tool calls in one shot instead of one reasoning round per call. " +
			"Provide exactly one of path or source.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Mode A (reuse): content path of a stored script program — its name for the entry content (e.g. \"ptc-report\"), or name/sub.md for a sub-content. Its entry content is the JavaScript executed in the sandbox. Provide this OR source, not both.",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Deprecated alias of path; prefer path (the same address the load/edit/write/search/delete tools take).",
				},
				"source": map[string]any{
					"type":        "string",
					"description": "Mode B (ad-hoc): the JavaScript source to run directly, nothing persisted. The body is async (top-level await works) and may use sdk.call/log/result/args; e.g. `const r = await sdk.call(\"workspace\", \"bash\", { command: \"ls\" }); sdk.result(r);`. Runs under the sandbox contract: no imports, no fs/process/network access. Provide this OR path, not both.",
				},
				"args": map[string]any{
					"type":        "object",
					"description": "Optional arguments object handed to the script as sdk.args (always an object; defaults to {}). Must be JSON-serializable. A stored script should read its inputs from here rather than hard-coding them.",
				},
				"timeout_seconds": map[string]any{
					"type":        "number",
					"description": "Optional wall-clock limit for the whole run in seconds (default 120), covering every nested call. On expiry the sandbox is killed, in-flight requests are abandoned, and the log collected so far is returned with the timeout error.",
				},
			},
			"required": []any{},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleRun(ctx, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeEdit,
		Response:    baseworker.StringSchema("confirmation"),
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
		Response:    baseworker.StringSchema("confirmation"),
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
		Response:    baseworker.StringSchema("confirmation"),
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
		Response:    baseworker.StringSchema("confirmation"),
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
		FormType    string   `json:"form_type,omitempty"`
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
			FormType:    string(p.EntryContent.FormType),
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

	w.ReplyCompleted(tc.CallerID, tc.CallID, results, tc.TraceID)
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
