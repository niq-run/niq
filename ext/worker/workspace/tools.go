package workspace

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
	backend "github.com/54c1/niq/ext/service/wsbackend"
)

// buildHandlers probes the backend's low-level interfaces and assembles
// tool handlers using backend helper functions. The worker checks what the
// backend can do and registers accordingly.
// maxBashTimeoutSec is the global hard cap on the bash tool's timeout
// argument: no caller can run a command longer than this, whatever they pass.
const maxBashTimeoutSec = 300 // 5 minutes

func (w *WorkspaceWorker) buildHandlers() {
	m := make(map[string]worker.ToolFunc)

	// ── File tools ──
	if fo, ok := w.backend.(backend.FileOperator); ok {
		m["read"] = func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			content, err := fo.Read(ctx, path, backend.GetIntArg(args, "offset", 0), backend.GetIntArg(args, "limit", 0))
			if err != nil {
				return "", err
			}
			return backend.FormatRead(path, content, args), nil
		}

		if w.mode != ModeReadOnly {
			m["write"] = func(ctx context.Context, args map[string]any) (string, error) {
				path, _ := args["path"].(string)
				content, _ := args["content"].(string)
				if path == "" {
					return "", fmt.Errorf("path is required")
				}
				if err := fo.Write(ctx, path, content); err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
			}

			m["edit"] = func(ctx context.Context, args map[string]any) (string, error) {
				path, _ := args["path"].(string)
				if path == "" {
					return "", fmt.Errorf("path is required")
				}
				rawEdits, ok := args["edits"].([]any)
				if !ok || len(rawEdits) == 0 {
					return "", fmt.Errorf("edits is required and must be a non-empty array")
				}
				for i, raw := range rawEdits {
					em, ok := raw.(map[string]any)
					if !ok {
						return "", fmt.Errorf("edit[%d]: expected object", i)
					}
					oldText, _ := em["old_text"].(string)
					newText, _ := em["new_text"].(string)
					if oldText == "" {
						return "", fmt.Errorf("edit[%d]: old_text is required", i)
					}
					if err := fo.Edit(ctx, path, oldText, newText); err != nil {
						return "", fmt.Errorf("edit[%d]: %w", i, err)
					}
				}
				return fmt.Sprintf("applied %d edit(s) to %s", len(rawEdits), path), nil
			}
		}
	}

	// ── Bash ──
	if bo, ok := w.backend.(backend.BashOperator); ok && w.mode == ModeFull {
		m["bash"] = func(ctx context.Context, args map[string]any) (string, error) {
			command, _ := args["command"].(string)
			if command == "" {
				return "", fmt.Errorf("command is required")
			}
			cwd, _ := args["cwd"].(string)

			timeoutSec := backend.GetIntArg(args, "timeout", 0)
			if timeoutSec > maxBashTimeoutSec {
				timeoutSec = maxBashTimeoutSec
			}
			if timeoutSec > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
				defer cancel()
			}

			var result backend.BashResult
			var err error

			limits := backend.BashLimits{MaxBytes: backend.MaxBashBytes, MaxLines: backend.MaxBashLines}
			onUpdate := backend.OnUpdate(ctx)
			if onUpdate != nil {
				result, err = bo.BashStream(ctx, command, cwd, onUpdate, limits)
			} else {
				result, err = bo.Bash(ctx, command, cwd, limits)
			}
			if err != nil {
				return "", err
			}
			return backend.FormatBash(result), nil
		}
	}

	// ── Search tools ──
	if gop, ok := w.backend.(backend.GrepOperator); ok {
		m["grep"] = func(ctx context.Context, args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			path, _ := args["path"].(string)
			include, _ := args["include"].(string)
			exclude, _ := args["exclude"].(string)
			return gop.Grep(ctx, pattern, path, include, exclude)
		}
	}

	if fop, ok := w.backend.(backend.FindOperator); ok {
		m["find"] = func(ctx context.Context, args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			path, _ := args["path"].(string)
			return fop.Find(ctx, path, pattern)
		}
	}

	if dl, ok := w.backend.(backend.DirLister); ok {
		m["ls"] = func(ctx context.Context, args map[string]any) (string, error) {
			dirPath, _ := args["path"].(string)
			if dirPath == "" {
				dirPath = "."
			}
			entries, err := dl.List(ctx, dirPath)
			if err != nil {
				return "", err
			}
			return backend.FormatLs(entries), nil
		}
	}

	w.handlers = m
}

// ── Tool dispatch ──

func (w *WorkspaceWorker) handleToolCall(ctx context.Context, evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	name, _ := evt.Payload["name"].(string)
	callerID := evt.WorkerId
	traceID := evt.TraceID

	log.Printf("[workspace %s] tool call: %s (id=%s)", w.ID(), name, callID)

	args, _ := evt.Payload["arguments"].(map[string]any)

	handler, ok := w.handlers[name]
	if !ok {
		w.publishFailed(callerID, callID, name, fmt.Errorf("unknown tool: %s", name), traceID)
		return
	}

	ctx = backend.WithOnUpdate(ctx, func(partial string) {
		evt := event.New(event.TypeToolPartial, w.ID(), map[string]any{
			"worker_id": callerID,
			"call_id":   callID,
			"name":      name,
			"partial":   partial,
		})
		evt.Transient = true // live-streaming only, not durable history
		_ = w.Channel.Send(context.Background(), evt, callerID)
	})

	result, err := handler(ctx, args)
	if err != nil {
		log.Printf("[workspace %s] tool error: %v", w.ID(), err)
		w.publishFailed(callerID, callID, name, err, traceID)
		return
	}

	w.publishCompleted(callerID, callID, name, result, traceID)
}

// ── Bus publishing ──

func (w *WorkspaceWorker) publishCompleted(callerID, callID, toolName, result, traceID string) {
	evt := event.New(event.TypeToolCompleted, w.ID(), map[string]any{
		"worker_id": callerID,
		"call_id":   callID,
		"name":      toolName,
		"result":    result,
	})
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
	log.Printf("[workspace %s] completed %s", w.ID(), callID)
}

func (w *WorkspaceWorker) publishFailed(callerID, callID, toolName string, err error, traceID string) {
	evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
		"worker_id": callerID,
		"call_id":   callID,
		"name":      toolName,
		"error":     err.Error(),
	})
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// publishReady announces available tools via worker.ready.
func (w *WorkspaceWorker) publishReady() {
	ts := make([]map[string]any, 0, len(w.handlers))

	if _, ok := w.handlers["read"]; ok {
		ts = append(ts, map[string]any{
			"name":        "read",
			"description": "Read the contents of a file within the workspace. Returns content with line numbers. Supports offset/limit pagination for large files.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "description": "Path to the file, relative to the workspace root."},
					"offset": map[string]any{"type": "integer", "description": "1-indexed line number to start reading from. Defaults to 1."},
					"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read. Defaults to 2000."},
				},
				"required": []any{"path"},
			},
		})
	}

	if _, ok := w.handlers["write"]; ok {
		ts = append(ts, map[string]any{
			"name":        "write",
			"description": "Write content to a file within the workspace. Overwrites the entire file. Creates parent directories if needed.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Path to the file, relative to the workspace root."},
					"content": map[string]any{"type": "string", "description": "Full content to write to the file."},
				},
				"required": []any{"path", "content"},
			},
		})
	}

	if _, ok := w.handlers["edit"]; ok {
		ts = append(ts, map[string]any{
			"name":        "edit",
			"description": "Edit a file by applying one or more find-and-replace operations. Each edit specifies old_text and new_text. Uses fuzzy Unicode quote normalization as fallback.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Path to the file, relative to the workspace root."},
					"edits": map[string]any{
						"type":        "array",
						"description": "Array of {old_text, new_text} objects to apply.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"old_text": map[string]any{"type": "string", "description": "The exact text to find."},
								"new_text": map[string]any{"type": "string", "description": "The replacement text."},
							},
							"required": []any{"old_text", "new_text"},
						},
					},
				},
				"required": []any{"path", "edits"},
			},
		})
	}

	if _, ok := w.handlers["bash"]; ok {
		ts = append(ts, map[string]any{
			"name":        "bash",
			"description": "Run a shell command within the workspace root directory. Returns exit code, stdout, and stderr. Output larger than 20KB is truncated to its head and tail. Supports optional timeout.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "The shell command to execute."},
					"cwd":     map[string]any{"type": "string", "description": "Working directory relative to workspace root."},
					"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (optional, capped at 300)."},
				},
				"required": []any{"command"},
			},
		})
	}

	if _, ok := w.handlers["grep"]; ok {
		ts = append(ts, map[string]any{
			"name":        "grep",
			"description": "Search files recursively using grep -rn. Returns file:line:content matches.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string", "description": "Regex pattern to search for."},
					"path":    map[string]any{"type": "string", "description": "Directory to search. Defaults to workspace root."},
					"include": map[string]any{"type": "string", "description": "File glob to include (e.g. *.go)."},
					"exclude": map[string]any{"type": "string", "description": "File glob to exclude (e.g. *_test.go)."},
				},
				"required": []any{"pattern"},
			},
		})
	}

	if _, ok := w.handlers["find"]; ok {
		ts = append(ts, map[string]any{
			"name":        "find",
			"description": "Find files by name glob using find -name.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Directory to search. Defaults to workspace root."},
					"pattern": map[string]any{"type": "string", "description": "Filename glob pattern (e.g. *.go)."},
				},
				"required": []any{"pattern"},
			},
		})
	}

	if _, ok := w.handlers["ls"]; ok {
		ts = append(ts, map[string]any{
			"name":        "ls",
			"description": "List directory contents. Returns a structured summary with entries marked as [file] or [dir].",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Directory to list. Defaults to workspace root."},
				},
			},
		})
	}

	evt := event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "workspace",
		"tools":     ts,
	})
	_ = w.Channel.Broadcast(context.Background(), evt)
	log.Printf("[workspace %s] published worker.ready with %d tools (mode=%d)", w.ID(), len(ts), w.mode)
}
