package workspace

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/baseworker"
	backend "github.com/niq-run/niq/pkg/services/wsbackend"
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
	w.registerExtensions()
}

// ── Bus publishing ──

func (w *WorkspaceWorker) publishCompleted(callerID, callID, toolName, result, traceID string) {
	evt := event.New(event.TypeRequestCompleted, w.ID(), map[string]any{
		"worker_id": callerID,
		"name":      toolName,
		"result":    result,
	})
	evt.RequestId = callID
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
	log.Printf("[workspace %s] completed %s", w.ID(), callID)
}

func (w *WorkspaceWorker) publishFailed(callerID, callID, toolName string, err error, traceID string) {
	evt := event.New(event.TypeRequestFailed, w.ID(), map[string]any{
		"worker_id": callerID,
		"name":      toolName,
		"error":     err.Error(),
	})
	evt.RequestId = callID
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// registerExtensions declares each available workspace tool as an extension
// served by its own event type (the tool name), announced to peers via
// AnnounceReady. Only tools whose handler is present (the mode/backend
// supports them) are registered.
func (w *WorkspaceWorker) registerExtensions() {
	ctx := context.Background()
	type spec struct {
		desc   string
		params map[string]any
	}
	specs := map[string]spec{
		"read": {
			desc: "Read the contents of a file within the workspace. Returns content with line numbers. Supports offset/limit pagination for large files.",
			params: map[string]any{"type": "object", "properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Path to the file, relative to the workspace root."},
				"offset": map[string]any{"type": "integer", "description": "1-indexed line number to start reading from. Defaults to 1."},
				"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read. Defaults to 2000."},
			}, "required": []any{"path"}},
		},
		"write": {
			desc: "Write content to a file within the workspace. Overwrites the entire file. Creates parent directories if needed.",
			params: map[string]any{"type": "object", "properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path to the file, relative to the workspace root."},
				"content": map[string]any{"type": "string", "description": "Full content to write to the file."},
			}, "required": []any{"path", "content"}},
		},
		"edit": {
			desc: "Edit a file by applying one or more find-and-replace operations. Each edit specifies old_text and new_text. Uses fuzzy Unicode quote normalization as fallback.",
			params: map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Path to the file, relative to the workspace root."},
				"edits": map[string]any{"type": "array", "description": "Array of {old_text, new_text} objects to apply.", "items": map[string]any{
					"type": "object", "properties": map[string]any{
						"old_text": map[string]any{"type": "string", "description": "The exact text to find."},
						"new_text": map[string]any{"type": "string", "description": "The replacement text."},
					}, "required": []any{"old_text", "new_text"}},
				},
			}, "required": []any{"path", "edits"}},
		},
		"bash": {
			desc: "Run a shell command within the workspace root directory. Returns exit code, stdout, and stderr. Output larger than 20KB is truncated to its head and tail. Supports optional timeout.",
			params: map[string]any{"type": "object", "properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The shell command to execute."},
				"cwd":     map[string]any{"type": "string", "description": "Working directory relative to workspace root."},
				"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (optional, capped at 300)."},
			}, "required": []any{"command"}},
		},
		"grep": {
			desc: "Search files recursively using grep -rn. Returns file:line:content matches.",
			params: map[string]any{"type": "object", "properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Regex pattern to search for."},
				"path":    map[string]any{"type": "string", "description": "Directory to search. Defaults to workspace root."},
				"include": map[string]any{"type": "string", "description": "File glob to include (e.g. *.go)."},
				"exclude": map[string]any{"type": "string", "description": "File glob to exclude (e.g. *_test.go)."},
			}, "required": []any{"pattern"}},
		},
		"find": {
			desc: "Find files by name glob using find -name.",
			params: map[string]any{"type": "object", "properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Directory to search. Defaults to workspace root."},
				"pattern": map[string]any{"type": "string", "description": "Filename glob pattern (e.g. *.go)."},
			}, "required": []any{"pattern"}},
		},
		"ls": {
			desc: "List directory contents. Returns a structured summary with entries marked as [file] or [dir].",
			params: map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory to list. Defaults to workspace root."},
			}},
		},
	}
	for name, sp := range specs {
		if _, ok := w.handlers[name]; !ok {
			continue
		}
		name := name
		w.Register(baseworker.Extension{Event: event.EventType(name), Description: sp.desc, Parameters: sp.params}, func(evt event.Event) {
			tc := baseworker.ParseToolCall(evt)
			w.dispatchHandler(ctx, tc)
		})
	}
}

// dispatchHandler serves a tool call through the registered handler map.
func (w *WorkspaceWorker) dispatchHandler(ctx context.Context, tc baseworker.ToolCall) {
	handler, ok := w.handlers[tc.Name]
	if !ok {
		w.ReplyFailed(tc.CallerID, tc.CallID, "unknown tool: "+tc.Name, tc.TraceID)
		return
	}
	result, err := handler(ctx, tc.Args)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, err.Error(), tc.TraceID)
		return
	}
	w.ReplyCompleted(tc.CallerID, tc.CallID, result, tc.TraceID)
}
