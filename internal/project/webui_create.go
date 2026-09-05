package project

import (
	"encoding/json"
	"fmt"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/internal/webui"
	"github.com/niq-run/niq/pkg/services/workerhost"
)

// webuiDeclCreator implements webui.WorkerDeclCreator: it persists a worker
// declaration from the WebUI's create form into project.json — seeding a
// managed worker's authoritative config.json — and launches it. External
// workers are provisioned and started here; managed workers come back with a
// spawn-event payload that the WebUI server sends to the host worker, so the
// spawn rides the auditable bus instead of a direct worker-service call.
type webuiDeclCreator struct {
	supervisor *UnmanagedSupervisor
	registry   corebus.IdentityRegistry
	workerSvc  *workerhost.WorkerService // may be nil; used for duplicate checks only
	projectID  string
}

// Create persists the declaration and launches the worker. The spawned config
// is validated against the existing declarations, the worker service and the
// bus registry — any of them already holding the id is an "already exists"
// error (the WebUI maps it to 409).
func (c *webuiDeclCreator) Create(body json.RawMessage) (webui.WorkerCreated, error) {
	if c.projectID == "" {
		return webui.WorkerCreated{}, fmt.Errorf("worker creation requires a project")
	}
	var wc WorkerConfig
	if err := json.Unmarshal(body, &wc); err != nil {
		return webui.WorkerCreated{}, fmt.Errorf("bad worker definition: %w", err)
	}
	if wc.Type == "" {
		return webui.WorkerCreated{}, fmt.Errorf("type is required")
	}
	if wc.ID == "" {
		return webui.WorkerCreated{}, fmt.Errorf("id is required")
	}
	if sanitizeID(wc.ID) != wc.ID {
		return webui.WorkerCreated{}, fmt.Errorf("id may only contain letters, digits, '-', '_', '.'")
	}

	p, err := LoadProject(c.projectID)
	if err != nil {
		return webui.WorkerCreated{}, err
	}
	if _, ok := FindWorker(p, wc.ID); ok {
		return webui.WorkerCreated{}, fmt.Errorf("worker %s already exists", wc.ID)
	}
	if c.workerSvc != nil && c.workerSvc.HasWorker(wc.ID) {
		return webui.WorkerCreated{}, fmt.Errorf("worker %s already exists", wc.ID)
	}
	if _, ok := c.registry.Lookup(wc.ID); ok {
		return webui.WorkerCreated{}, fmt.Errorf("worker %s already exists", wc.ID)
	}

	if isManagedWorker(wc) {
		// The builder reads everything from Params, so the authoritative
		// config.json (seeded from the form) is the whole story; the
		// project.json entry stays a light reference, like CreateProject's.
		if err := seedWorkerConfig(ProjectDir(c.projectID), wc); err != nil {
			return webui.WorkerCreated{}, err
		}
		p.Workers = append(p.Workers, ProjectWorker{ID: wc.ID, Type: wc.Type, Managed: true})
		if err := SaveProject(p); err != nil {
			return webui.WorkerCreated{}, err
		}
		payload := map[string]any{"type": wc.Type, "id": wc.ID}
		for k, v := range workerConfigParams(wc) {
			payload[k] = v
		}
		return webui.WorkerCreated{ID: wc.ID, Type: wc.Type, Managed: true, Spawn: payload}, nil
	}

	if len(wc.Command) == 0 {
		return webui.WorkerCreated{}, fmt.Errorf("command is required for an external worker")
	}
	spec := ProjectWorker{
		ID:            wc.ID,
		Type:          wc.Type,
		Command:       wc.Command,
		Env:           wc.Env,
		Cwd:           wc.Cwd,
		Subscriptions: wc.Subscriptions,
		Publish:       wc.Publish,
	}
	p.Workers = append(p.Workers, spec)
	if err := SaveProject(p); err != nil {
		return webui.WorkerCreated{}, err
	}
	if err := provisionUnmanaged(c.registry, c.projectID, &spec); err != nil {
		return webui.WorkerCreated{}, err
	}
	if c.supervisor == nil {
		return webui.WorkerCreated{}, fmt.Errorf("worker creation requires a bus")
	}
	if err := c.supervisor.Start(spec); err != nil {
		return webui.WorkerCreated{}, err
	}
	return webui.WorkerCreated{ID: wc.ID, Type: wc.Type}, nil
}

// ManagedSpawn returns the spawn-event payload for a declared managed worker,
// built from its authoritative config.json. managed is false when the id is
// not a declared managed worker (the caller falls through to the external
// start path).
func (c *webuiDeclCreator) ManagedSpawn(id string) (map[string]any, bool, error) {
	if c.projectID == "" {
		return nil, false, fmt.Errorf("worker creation requires a project")
	}
	p, err := LoadProject(c.projectID)
	if err != nil {
		return nil, false, err
	}
	spec, ok := FindWorker(p, id)
	if !ok || !spec.Managed {
		return nil, false, nil
	}
	cfg, ok := readWorkerConfig(ProjectDir(c.projectID), id)
	if !ok {
		return nil, false, fmt.Errorf("worker %s: config.json missing; cannot spawn", id)
	}
	payload := map[string]any{"type": cfg.Type, "id": cfg.ID}
	for k, v := range cfg.Params {
		payload[k] = v
	}
	return payload, true, nil
}
