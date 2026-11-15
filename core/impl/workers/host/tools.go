package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
)

// The host worker's tools, each its own event type.
const (
	TypeSpawn   event.EventType = "spawn"
	TypeSuspend event.EventType = "suspend"
	TypeResume  event.EventType = "resume"
	TypeRestart event.EventType = "restart"
	TypeDestroy event.EventType = "destroy"
)

// registerExtensions declares the host tools: each is an extension served by
// its own event type, announced to peers via AnnounceReady.
func (w *HostWorker) registerExtensions() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.Register(baseworker.Extension{
		Event:       TypeSpawn,
		Response:    baseworker.ObjectSchema(map[string]any{"worker_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"created"}}, "type": map[string]any{"type": "string"}, "placement": map[string]any{"type": "string"}}),
		Description: "Create a worker and start it immediately. `type` selects the kind (reason, workspace, mcp, ...); `placement` selects where it runs: in-process (default, host builds it), managed-process (host launches the command and supervises the OS process), or unmanaged-process (a third party dials in on its own; nothing is launched here). Type-specific arguments are passed as top-level properties. For managed-process, every argument other than the fixed fields (type/id/placement/command/cwd/label/tags/description) becomes a child environment variable. Parameters are validated per type: one the type does not read fails the call and the error lists what that type accepts.",
		Parameters: obj(map[string]any{
			"type": map[string]any{
				"type":        "string",
				"description": "Worker type label (reason, workspace, mcp, timer, ...). It names the identity and, for in-process placement, the builder to run.",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Optional worker ID. Builders may derive a default (e.g. workspace from path); the reply carries the id actually used.",
			},
			"placement": map[string]any{
				"type":        "string",
				"enum":        []string{"in-process", "managed-process", "unmanaged-process"},
				"description": "Where the worker runs. in-process (default): built by the host and runs inside the host process. managed-process: the host launches `command` as an OS process and supervises it (crash restart, start/stop). unmanaged-process: the third party starts itself and dials the bus; the host only declares it and issues a bus credential, which is NOT returned here.",
			},
			"command": map[string]any{
				"type":        "array",
				"description": "Required for placement=managed-process: argv of the process, e.g. [\"npx\",\"-y\",\"@modelcontextprotocol/server-filesystem\",\"/data\"]. Rejected for other placements.",
				"items":       map[string]any{"type": "string"},
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory for a managed-process child (must exist).",
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
				"items":       map[string]any{"type": "string"},
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Optional short note explaining what this worker is for. Display metadata only.",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional display name (备注名) shown in the WebUI in place of the worker id. Display metadata only; the id stays the identity on the bus.",
			},
			"collaborators": map[string]any{
				"type":        "array",
				"description": "When type=reason/coding: the reason workers this one may collaborate with. Omitted means every reason worker is visible; when given it is an allowlist (empty = none). An out-of-scope reason peer is invisible to this worker and cannot be messaged or called; capability workers are unaffected.",
				"items":       map[string]any{"type": "string"},
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		result, err := w.handleSpawn(tc)
		w.reply(evt, result, err)
	})

	w.Register(baseworker.Extension{
		Event:       TypeSuspend,
		Response:    baseworker.ObjectSchema(map[string]any{"worker_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"suspended"}}}),
		Description: "Suspend a running worker: an in-process worker is snapshotted, stopped and its bus connection released; a managed-process worker's OS process is stopped (it persists its own state). The worker stays declared and can be resumed.",
		Parameters: obj(map[string]any{
			"worker_id": map[string]any{
				"type":        "string",
				"description": "ID of the worker to suspend.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		result, err := w.handleSuspend(tc)
		w.reply(evt, result, err)
	})

	w.Register(baseworker.Extension{
		Event:       TypeResume,
		Response:    baseworker.ObjectSchema(map[string]any{"worker_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"running"}}}),
		Description: "Resume a worker that is declared but not running: an in-process worker reconnects and restores its snapshot; a managed-process worker is relaunched; an unmanaged-process worker only gets its bus identity re-ensured (it starts itself).",
		Parameters: obj(map[string]any{
			"worker_id": map[string]any{
				"type":        "string",
				"description": "ID of the worker to resume.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		result, err := w.handleResume(tc)
		w.reply(evt, result, err)
	})

	w.Register(baseworker.Extension{
		Event:       TypeRestart,
		Response:    baseworker.ObjectSchema(map[string]any{"worker_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"running"}}}),
		Description: "Restart a worker from its declaration. Construction params follow the declaration edit; what the worker itself persisted survives (an in-process worker's snapshot, a managed-process worker's own state directory).",
		Parameters: obj(map[string]any{
			"worker_id": map[string]any{
				"type":        "string",
				"description": "ID of the worker to restart.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		result, err := w.handleRestart(tc)
		w.reply(evt, result, err)
	})

	w.Register(baseworker.Extension{
		Event:       TypeDestroy,
		Response:    baseworker.ObjectSchema(map[string]any{"worker_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"destroyed"}}}),
		Description: "Destroy a worker: stop it, delete its declaration, its persisted state, its bus connection and its identity. This is irreversible. The removal happens immediately.",
		Parameters: obj(map[string]any{
			"worker_id": map[string]any{
				"type":        "string",
				"description": "ID of the worker to destroy.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		result, err := w.handleDestroy(tc)
		w.reply(evt, result, err)
	})
}

// reply answers a tool invocation with request.completed / request.failed.
func (w *HostWorker) reply(evt event.Event, result any, err error) {
	if err != nil {
		w.ReplyFailed(evt.WorkerId, evt.RequestId, err.Error(), evt.TraceID)
		return
	}
	w.ReplyCompleted(evt.WorkerId, evt.RequestId, result, evt.TraceID)
}

// liftDisplayMeta strips the display-only spawn arguments (label / tags /
// description) out of the construction params and returns them, so builders
// never see them. They travel on typed fields and land in the project
// declaration for the WebUI selector/filter; the params map is mutated in place
// and keeps every type-specific key.
func liftDisplayMeta(args map[string]any) (label string, tags []string, desc string) {
	label = strings.TrimSpace(baseworker.ArgString(args, "label"))
	tags = baseworker.ArgStrings(args, "tags")
	desc = baseworker.ArgString(args, "description")
	delete(args, "label")
	if tags != nil {
		delete(args, "tags")
	}
	if desc != "" {
		delete(args, "description")
	}
	return label, tags, desc
}

// liftPlacement strips the placement/launch arguments out of the params map:
// they describe HOW the worker runs, not what type it is, so a builder must
// never see them.
// liftACL pulls the optional publish/subscriptions grants out of the spawn
// args. They are identity data, not construction params.
func liftACL(args map[string]any) (pub []event.PublishPattern, sub []event.EventPattern) {
	if raw, ok := args["publish"]; ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &pub)
		delete(args, "publish")
	}
	if raw, ok := args["subscriptions"]; ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &sub)
		delete(args, "subscriptions")
	}
	return pub, sub
}

func liftPlacement(args map[string]any) (placement worker.Placement, command []string, cwd string) {
	placement = worker.Placement(strings.TrimSpace(baseworker.ArgString(args, "placement")))
	command = baseworker.ArgStrings(args, "command")
	cwd = baseworker.ArgString(args, "cwd")
	delete(args, "placement")
	delete(args, "command")
	delete(args, "cwd")
	return placement, command, cwd
}

func (w *HostWorker) handleSpawn(tc baseworker.ToolCall) (any, error) {
	typ := baseworker.ArgString(tc.Args, "type")
	if typ == "" {
		return "", fmt.Errorf("type is required")
	}
	id := baseworker.ArgString(tc.Args, "id")
	// type/id are identity fields, not construction params. The declaration
	// carries them; a builder must not see them inside params (and a
	// managed-process child must not receive them as env).
	delete(tc.Args, "type")
	delete(tc.Args, "id")

	label, tags, desc := liftDisplayMeta(tc.Args)
	placement, command, cwd := liftPlacement(tc.Args)
	publish, subscribe := liftACL(tc.Args)
	spec := worker.WorkerSpec{
		Placement: placement,
		Command:   command,
		Cwd:       cwd,
	}
	spec.ID = id
	spec.Type = typ
	spec.Params = tc.Args
	spec.Label = label
	spec.Tags = tags
	spec.Description = desc
	spec.PublishAllow = publish
	spec.SubscribeAllow = subscribe
	info, err := w.engine.Create(context.Background(), spec)
	if err != nil {
		return "", err
	}
	return map[string]any{
		"worker_id": info.ID,
		"status":    "created",
		"type":      info.Type,
		"placement": string(info.Placement),
	}, nil
}

// workerIDArg pulls the target id out of a lifecycle call.
func workerIDArg(tc baseworker.ToolCall) (string, error) {
	id := baseworker.ArgString(tc.Args, "worker_id")
	if id == "" {
		return "", fmt.Errorf("worker_id is required")
	}
	return id, nil
}

func (w *HostWorker) handleSuspend(tc baseworker.ToolCall) (any, error) {
	id, err := workerIDArg(tc)
	if err != nil {
		return "", err
	}
	if err := w.engine.Suspend(context.Background(), id); err != nil {
		return "", err
	}
	return map[string]any{"worker_id": id, "status": "suspended"}, nil
}

func (w *HostWorker) handleResume(tc baseworker.ToolCall) (any, error) {
	id, err := workerIDArg(tc)
	if err != nil {
		return "", err
	}
	if err := w.engine.Resume(context.Background(), id); err != nil {
		return "", err
	}
	return map[string]any{"worker_id": id, "status": "running"}, nil
}

func (w *HostWorker) handleDestroy(tc baseworker.ToolCall) (any, error) {
	id, err := workerIDArg(tc)
	if err != nil {
		return "", err
	}
	if w.engine.Protected(id) {
		return "", fmt.Errorf("worker %s is protected and cannot be destroyed", id)
	}
	if err := w.engine.Destroy(context.Background(), id); err != nil {
		return "", err
	}
	return map[string]any{"worker_id": id, "status": "destroyed"}, nil
}

func (w *HostWorker) handleRestart(tc baseworker.ToolCall) (any, error) {
	id, err := workerIDArg(tc)
	if err != nil {
		return "", err
	}
	if err := w.engine.Restart(context.Background(), id); err != nil {
		return "", err
	}
	return map[string]any{"worker_id": id, "status": "running"}, nil
}
