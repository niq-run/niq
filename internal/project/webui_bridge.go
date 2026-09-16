// WebUI ↔ project bridge: the adapters the project WebUI server is wired with,
// mapping webui interface methods onto project.json declarations, the worker
// service and the unmanaged supervisor. run.go only assembles these; the
// project-specific logic lives here.
package project

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/internal/webui"
	"github.com/niq-run/niq/pkg/services/workerhost"
)

// webuiDeclCreator implements webui.WorkerDeclCreator: it persists a worker
// declaration from the WebUI's create form into project.json and launches it.
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
	if err := validateWorkerMeta(wc); err != nil {
		return webui.WorkerCreated{}, err
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

	wc = instantiateWorker(wc, c.projectID)

	if isManagedWorker(wc) {
		// The declaration carries the whole config, so persisting it is all
		// it takes for the worker to survive a restart; the spawn payload
		// rides the bus and the host worker creates the live instance.
		p.Workers = append(p.Workers, wc)
		if err := SaveProject(p); err != nil {
			return webui.WorkerCreated{}, err
		}
		payload := map[string]any{"type": wc.Type, "id": wc.ID}
		for k, v := range workerParams(wc) {
			payload[k] = v
		}
		return webui.WorkerCreated{ID: wc.ID, Type: wc.Type, Managed: true, Spawn: payload}, nil
	}

	if len(wc.Command) == 0 {
		return webui.WorkerCreated{}, fmt.Errorf("command is required for an external worker")
	}
	p.Workers = append(p.Workers, wc)
	if err := SaveProject(p); err != nil {
		return webui.WorkerCreated{}, err
	}
	if err := provisionUnmanaged(c.registry, c.projectID, &wc); err != nil {
		return webui.WorkerCreated{}, err
	}
	if c.supervisor == nil {
		return webui.WorkerCreated{}, fmt.Errorf("worker creation requires a bus")
	}
	if err := c.supervisor.Start(wc); err != nil {
		return webui.WorkerCreated{}, err
	}
	return webui.WorkerCreated{ID: wc.ID, Type: wc.Type}, nil
}

// validateWorkerMeta checks a worker's display metadata (tags / description)
// from the create form. Tags form a slash-path hierarchy, so each segment may
// carry letters, digits and '_-.'; the slash is the path separator. Segments
// must be non-empty (no "//", no leading/trailing slash). Description is free
// text but trimmed; empty values are dropped.
func validateWorkerMeta(wc WorkerConfig) error {
	for _, t := range wc.Tags {
		t = strings.TrimSpace(t)
		if t == "" {
			return fmt.Errorf("tags may not be empty")
		}
		for _, seg := range strings.Split(t, "/") {
			if seg == "" {
				return fmt.Errorf("tag %q: empty segment (no leading/trailing/double slash)", t)
			}
			for _, r := range seg {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
					return fmt.Errorf("tag %q: segments may only contain letters, digits, '-', '_', '.'", t)
				}
			}
		}
	}
	return nil
}

// ManagedSpawn returns the spawn-event payload for a declared managed worker,
// built from its project.json declaration. managed is false when the id is not
// a declared managed worker (the caller falls through to the external start
// path).
func (c *webuiDeclCreator) ManagedSpawn(id string) (map[string]any, bool, error) {
	if c.projectID == "" {
		return nil, false, fmt.Errorf("worker creation requires a project")
	}
	p, err := LoadProject(c.projectID)
	if err != nil {
		return nil, false, err
	}
	wc, ok := FindWorker(p, id)
	if !ok || !isManagedWorker(wc) {
		return nil, false, nil
	}
	payload := map[string]any{"type": wc.Type, "id": wc.ID}
	for k, v := range workerParams(wc) {
		payload[k] = v
	}
	return payload, true, nil
}

// workerMetaFromDecl builds the WebUI's worker display-metadata map (id ⇒
// tags / description) from the declared worker set, for the workers view and
// the talk target picker. Only declared hosts carry metadata.
func workerMetaFromDecl(workers []WorkerConfig) map[string]webui.WorkerMeta {
	if len(workers) == 0 {
		return nil
	}
	m := make(map[string]webui.WorkerMeta, len(workers))
	for _, wc := range workers {
		if wc.Tags == nil && wc.Description == "" {
			continue
		}
		m[wc.ID] = webui.WorkerMeta{Tags: wc.Tags, Description: wc.Description}
	}
	return m
}

// webuiMetaUpdater implements webui.WorkerMetaUpdater: it persists a worker's
// display metadata (tags / description) to its project.json declaration. Tags
// are pure WebUI management metadata — the running worker ignores them, so
// this is a declaration edit only, with no live worker operation.
type webuiMetaUpdater struct {
	projectID string
}

func (u webuiMetaUpdater) UpdateWorkerMeta(id string, meta webui.WorkerMeta) error {
	if u.projectID == "" {
		return fmt.Errorf("worker metadata update requires a project")
	}
	return UpdateWorkerMeta(u.projectID, id, meta.Tags, meta.Description)
}

// webuiDeclRemover implements webui.WorkerDeclRemover for a specific project,
// letting the WebUI remove a managed worker's project.json declaration on delete.
type webuiDeclRemover struct {
	projectID string
}

func (r webuiDeclRemover) RemoveDecl(id string) error {
	if err := RemoveWorkerDecl(r.projectID, id); err != nil {
		return err
	}
	return removeWorkerStateDir(r.projectID, id)
}

// webuiUnmanagedAdapter implements webui.UnmanagedController, routing the
// project WebUI's external-worker controls to the project supervisor.
type webuiUnmanagedAdapter struct {
	supervisor *UnmanagedSupervisor
	registry   corebus.IdentityRegistry
	workerSvc  *workerhost.WorkerService // optional; identifies live managed workers
	projectID  string
}

func (a *webuiUnmanagedAdapter) Start(id string) error {
	if a.projectID == "" {
		return fmt.Errorf("unmanaged control requires a project")
	}
	p, err := LoadProject(a.projectID)
	if err != nil {
		return err
	}
	spec, ok := FindWorker(p, id)
	if !ok {
		return fmt.Errorf("worker %s not found", id)
	}
	if isManagedWorker(spec) {
		return fmt.Errorf("worker %s is managed, not an external process", id)
	}
	if err := provisionUnmanaged(a.registry, a.projectID, &spec); err != nil {
		return err
	}
	return a.supervisor.Start(spec)
}

func (a *webuiUnmanagedAdapter) Stop(id string) error    { return a.supervisor.Stop(id) }
func (a *webuiUnmanagedAdapter) Restart(id string) error { return a.supervisor.Restart(id) }

// Remove stops the worker (if supervised) and deletes its project.json
// declaration so it is not relaunched next start.
func (a *webuiUnmanagedAdapter) Remove(id string) error {
	_ = a.supervisor.Stop(id)
	if a.projectID == "" {
		return fmt.Errorf("unmanaged control requires a project")
	}
	if err := RemoveWorkerDecl(a.projectID, id); err != nil {
		return err
	}
	return removeWorkerStateDir(a.projectID, id)
}

func (a *webuiUnmanagedAdapter) List() []webui.UnmanagedStatus {
	out := make([]webui.UnmanagedStatus, 0, 4)
	for _, st := range a.supervisor.List() {
		out = append(out, webui.UnmanagedStatus{ID: st.ID, Type: st.Type, State: st.State, Alive: st.Alive})
	}
	return out
}

// Declared returns every worker project.json declares, merging the
// supervisor's live state so declared-but-not-started externals show as
// stopped. Managed declarations always report "stopped" — a live managed
// worker's state belongs to the worker service, and the WebUI filters these
// out via the registry / workerSvc before rendering. They drive the UI's
// start buttons for declared-but-idle workers of both kinds.
func (a *webuiUnmanagedAdapter) Declared() []webui.UnmanagedStatus {
	if a.projectID == "" {
		return nil
	}
	p, err := LoadProject(a.projectID)
	if err != nil {
		return nil
	}
	running := map[string]bool{}
	for _, st := range a.supervisor.List() {
		running[st.ID] = st.State == "running"
	}
	var out []webui.UnmanagedStatus
	for _, spec := range p.Workers {
		managed := isManagedWorker(spec)
		st := webui.UnmanagedStatus{ID: spec.ID, Type: spec.Type, Managed: managed, State: "stopped"}
		if !managed && running[spec.ID] {
			st.State = "running"
			st.Alive = true
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
