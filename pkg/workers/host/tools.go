package host

import (
	"context"
	"fmt"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/baseworker"
)

// The host worker's tools, each its own event type.
const (
	TypeSpawn   event.EventType = "spawn"
	TypeSuspend event.EventType = "suspend"
	TypeResume  event.EventType = "resume"
)

// registerExtensions declares the host tools: each is an extension served by
// its own event type, announced to peers via AnnounceReady.
func (w *HostWorker) registerExtensions() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.Register(baseworker.Extension{
		Event:       TypeSpawn,
		Description: "Create a new worker. Type determines worker kind (reason, workspace, ...). Type-specific arguments (e.g. path, provider, model, programs, events) are passed as top-level properties.",
		Parameters: obj(map[string]any{
			"type": map[string]any{
				"type":        "string",
				"description": "Worker type to spawn (reason, workspace, ...).",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Optional worker ID. Builders may derive a default (e.g. workspace from path).",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Workspace mount directory (required when type=workspace). Becomes the primary mount; add more at runtime via the workspace's mount.add.",
			},
			"mounts": map[string]any{
				"type":        "array",
				"description": "Workspace mount directories (when type=workspace); the first is the primary mount. Overrides path.",
				"items": map[string]any{
					"type": "string",
				},
			},
			"approver": map[string]any{
				"type":        "string",
				"description": "Worker that approves boundary-expansion requests (when type=workspace). Defaults to webui-hiw; empty disables the approval flow.",
			},
			"tags": map[string]any{
				"type":        "array",
				"description": "Optional slash-path labels that group this worker in the WebUI selector (e.g. [\"ops/backup\", \"work\"]). Display metadata only; carried to the declaration so it can be found and filtered-by in the UI. A comma-separated string is also accepted.",
				"items": map[string]any{"type": "string"},
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Optional short note explaining what this worker is for. Display metadata only.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		result, err := w.handleSpawn(tc)
		w.reply(evt, string(TypeSpawn), result, err)
	})

	w.Register(baseworker.Extension{
		Event:       TypeSuspend,
		Description: "Suspend a running worker: snapshots its state, stops its goroutine and releases its bus connection. The worker can later be resumed.",
		Parameters: obj(map[string]any{
			"worker_id": map[string]any{
				"type":        "string",
				"description": "ID of the worker to suspend.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		result, err := w.handleSuspend(tc)
		w.reply(evt, string(TypeSuspend), result, err)
	})

	w.Register(baseworker.Extension{
		Event:       TypeResume,
		Description: "Resume a suspended worker: reconnects its bus channel, restores its snapshot and starts it.",
		Parameters: obj(map[string]any{
			"worker_id": map[string]any{
				"type":        "string",
				"description": "ID of the worker to resume.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		result, err := w.handleResume(tc)
		w.reply(evt, string(TypeResume), result, err)
	})
}

// reply answers a tool invocation with request.completed / request.failed.
func (w *HostWorker) reply(evt event.Event, toolName, result string, err error) {
	if err != nil {
		w.ReplyFailed(evt.WorkerId, evt.RequestId, err.Error(), evt.TraceID)
		return
	}
	w.ReplyCompleted(evt.WorkerId, evt.RequestId, result, evt.TraceID)
}

// handleSpawn forwards the spawn request to the WorkerService, which
// dispatches to the registered builder for the requested type.
func (w *HostWorker) handleSpawn(tc baseworker.ToolCall) (string, error) {
	typ := baseworker.ArgString(tc.Args, "type")
	if typ == "" {
		return "", fmt.Errorf("type is required")
	}
	id := baseworker.ArgString(tc.Args, "id")

	// Lift display metadata (tags / description) out of the construction
	// params so builders never see them: they travel on typed fields and land
	// in the project.json declaration for the WebUI selector/filter.
	tags := baseworker.ArgStrings(tc.Args, "tags")
	desc := baseworker.ArgString(tc.Args, "description")
	params := tc.Args
	if tags != nil {
		delete(params, "tags")
	}
	if desc != "" {
		delete(params, "description")
	}

	cfg := worker.WorkerConfig{ID: id, Type: typ, Params: params, Tags: tags, Description: desc}
	if err := w.engine.CreateWorker(context.Background(), cfg); err != nil {
		return "", err
	}
	// The builder may derive a default ID (e.g. workspace from path).
	info, _ := w.engine.WorkerType(id)
	return fmt.Sprintf(`{"worker_id":%q,"status":"created","type":%q}`, id, info), nil
}

func (w *HostWorker) handleSuspend(tc baseworker.ToolCall) (string, error) {
	id := baseworker.ArgString(tc.Args, "worker_id")
	if id == "" {
		return "", fmt.Errorf("worker_id is required")
	}
	if err := w.engine.SuspendWorker(id); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"worker_id":%q,"status":"suspended"}`, id), nil
}

func (w *HostWorker) handleResume(tc baseworker.ToolCall) (string, error) {
	id := baseworker.ArgString(tc.Args, "worker_id")
	if id == "" {
		return "", fmt.Errorf("worker_id is required")
	}
	if err := w.engine.ResumeWorker(context.Background(), id); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"worker_id":%q,"status":"running"}`, id), nil
}
