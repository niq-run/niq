package host

import (
	"context"
	"fmt"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/worker"
)

func (w *HostWorker) handleToolCall(evt event.Event) {
	tc := worker.ParseToolCall(evt)

	var result string
	var err error

	switch tc.Name {
	case "spawn":
		result, err = w.handleSpawn(tc)
	case "suspend":
		result, err = w.handleSuspend(tc)
	case "resume":
		result, err = w.handleResume(tc)
	default:
		w.ReplyUnknownTool(tc)
		return
	}

	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, err.Error(), tc.TraceID)
		return
	}
	w.ReplyCompleted(tc.CallerID, tc.CallID, tc.Name, result, tc.TraceID)
}

// handleSpawn forwards the spawn request to the WorkerService, which
// dispatches to the registered builder for the requested type.
func (w *HostWorker) handleSpawn(tc worker.ToolCall) (string, error) {
	typ := worker.ArgString(tc.Args, "type")
	if typ == "" {
		return "", fmt.Errorf("type is required")
	}
	id := worker.ArgString(tc.Args, "id")
	cfg := worker.WorkerConfig{ID: id, Type: typ, Params: tc.Args}
	if err := w.engine.CreateWorker(context.Background(), cfg); err != nil {
		return "", err
	}
	// The builder may derive a default ID (e.g. workspace from path).
	info, _ := w.engine.WorkerType(id)
	return fmt.Sprintf(`{"worker_id":%q,"status":"created","type":%q}`, id, info), nil
}

func (w *HostWorker) handleSuspend(tc worker.ToolCall) (string, error) {
	id := worker.ArgString(tc.Args, "worker_id")
	if id == "" {
		return "", fmt.Errorf("worker_id is required")
	}
	if err := w.engine.SuspendWorker(id); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"worker_id":%q,"status":"suspended"}`, id), nil
}

func (w *HostWorker) handleResume(tc worker.ToolCall) (string, error) {
	id := worker.ArgString(tc.Args, "worker_id")
	if id == "" {
		return "", fmt.Errorf("worker_id is required")
	}
	if err := w.engine.ResumeWorker(context.Background(), id); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"worker_id":%q,"status":"running"}`, id), nil
}

// ── Bus publishing ──

func (w *HostWorker) publishReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "host",
		"tools": []map[string]any{
			{
				"name":        "spawn",
				"description": "Create a new worker. Type determines worker kind (reason, workspace, ...). Type-specific arguments (e.g. path, provider, model, programs, events) are passed as top-level properties.",
				"parameters": map[string]any{
					"type":                 "object",
					"description":          "Worker type and type-specific construction arguments.",
					"additionalProperties": true,
					"properties": map[string]any{
						"type": map[string]any{
							"type":        "string",
							"description": "Worker type to spawn.",
						},
						"id": map[string]any{
							"type":        "string",
							"description": "Optional worker ID. Builders may derive a default (e.g. workspace from path).",
						},
					},
					"required": []any{"type"},
				},
			},
			{
				"name":        "suspend",
				"description": "Suspend a running worker: snapshots its state, stops its goroutine and releases its bus connection. The worker can later be resumed.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"worker_id": map[string]any{
							"type":        "string",
							"description": "ID of the worker to suspend.",
						},
					},
					"required": []any{"worker_id"},
				},
			},
			{
				"name":        "resume",
				"description": "Resume a suspended worker: reconnects its bus channel, restores its snapshot and starts it.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"worker_id": map[string]any{
							"type":        "string",
							"description": "ID of the worker to resume.",
						},
					},
					"required": []any{"worker_id"},
				},
			},
		},
	}))
}
