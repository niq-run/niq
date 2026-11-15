// WebUI ↔ project bridge: the adapters the project WebUI server is wired with,
// mapping webui interface methods onto worker declarations.
//
// The WebUI never drives a lifecycle directly. Creating a worker persists its
// declaration here; STARTING it (create/start/restart/stop/delete) is a bus
// request to the host worker, so the UI, an agent and the boot-time recovery
// all walk the same lifecycle path.
package project

import (
	"encoding/json"
	"fmt"

	projworker "github.com/niq-run/niq/app/project/worker"
	"github.com/niq-run/niq/app/webui"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// webuiDeclCreator implements webui.WorkerDeclCreator: it validates a worker
// declaration from the WebUI's create form and persists it into workers.json.
// It launches nothing — the caller sends the host worker a `resume` for the new
// id, which is what materializes it (in whatever placement the declaration
// names). A launch that fails therefore leaves a declared-but-stopped worker
// the start button (the same `resume`) can retry.
type webuiDeclCreator struct {
	store     projworker.DeclStore
	projectID string
}

func (c *webuiDeclCreator) Create(body json.RawMessage) (webui.WorkerCreated, error) {
	if c.projectID == "" {
		return webui.WorkerCreated{}, fmt.Errorf("worker creation requires a project")
	}
	var wc WorkerConfig
	if err := json.Unmarshal(body, &wc); err != nil {
		return webui.WorkerCreated{}, fmt.Errorf("bad worker definition: %w", err)
	}
	wc = projworker.Instantiate(wc, c.projectID)
	if err := validateDeclaration(wc); err != nil {
		return webui.WorkerCreated{}, err
	}
	if _, ok, err := c.store.Get(wc.ID); err != nil {
		return webui.WorkerCreated{}, err
	} else if ok {
		return webui.WorkerCreated{}, fmt.Errorf("worker %s already exists", wc.ID)
	}
	if err := c.store.Add(&wc); err != nil {
		return webui.WorkerCreated{}, err
	}

	placement := projworker.PlacementOf(wc)
	created := webui.WorkerCreated{
		ID:        wc.ID,
		Type:      wc.Type,
		Managed:   placement == coreworker.PlacementInProcess,
		Remote:    placement == coreworker.PlacementUnmanagedProcess,
		Placement: string(placement),
	}
	// A dial-in worker's credential is issued when it is resumed. The UI shows
	// it so the third party can connect; it is read from the declaration, never
	// returned by the host's bus reply.
	if placement == coreworker.PlacementUnmanagedProcess && wc.Credential != "" {
		created.Credential = wc.Credential
	}
	return created, nil
}

// validateDeclaration checks everything that can be checked before a worker
// exists: identity, display metadata, placement consistency, and the launch
// spec a process worker needs.
func validateDeclaration(wc WorkerConfig) error {
	if wc.Type == "" {
		return fmt.Errorf("type is required")
	}
	if wc.ID == "" {
		return fmt.Errorf("id is required")
	}
	if projworker.SanitizeID(wc.ID) != wc.ID {
		return fmt.Errorf("id may only contain letters, digits, '-', '_', '.'")
	}
	if err := projworker.ValidateMeta(wc); err != nil {
		return err
	}
	switch projworker.PlacementOf(wc) {
	case coreworker.PlacementInProcess:
		if len(wc.Command) > 0 || wc.Cwd != "" {
			return fmt.Errorf("an in-process worker does not take command/cwd")
		}
	case coreworker.PlacementManagedProcess:
		if len(wc.Command) == 0 {
			return fmt.Errorf("command is required for a managed-process worker")
		}
		if err := projworker.ValidateProcessParams(wc.Params); err != nil {
			return err
		}
	case coreworker.PlacementUnmanagedProcess:
		if len(wc.Command) > 0 {
			return fmt.Errorf("an unmanaged-process worker starts itself; it must not carry a command")
		}
		if len(wc.Params) > 0 {
			return fmt.Errorf("an unmanaged-process worker cannot carry params: the host has no channel to pass them")
		}
	default:
		return fmt.Errorf("unknown placement %q", wc.Placement)
	}
	return nil
}

// workerMetaFromDecl builds the WebUI's worker display-metadata map (id ⇒
// label / tags / description) from the declared worker set, for the workers
// view and the talk target picker. Only declared hosts carry metadata.
func workerMetaFromDecl(workers []WorkerConfig) map[string]webui.WorkerMeta {
	if len(workers) == 0 {
		return nil
	}
	m := make(map[string]webui.WorkerMeta, len(workers))
	for _, wc := range workers {
		if wc.Label == "" && wc.Tags == nil && wc.Description == "" {
			continue
		}
		m[wc.ID] = webui.WorkerMeta{Label: wc.Label, Tags: wc.Tags, Description: wc.Description}
	}
	return m
}

// webuiParamsProvider implements webui.WorkerParamsProvider: it returns the
// declared construction params (`params`) of every active worker, read
// fresh each call so the worker view reflects runtime create/delete.
type webuiParamsProvider struct {
	projectID string
}

func (p webuiParamsProvider) WorkerParamsMap() map[string]map[string]any {
	set, err := LoadWorkers(p.projectID)
	if err != nil {
		return nil
	}
	m := make(map[string]map[string]any)
	for _, wc := range set.Workers {
		if len(wc.Params) > 0 {
			m[wc.ID] = wc.Params
		}
	}
	return m
}

// webuiMetaUpdater implements webui.WorkerMetaUpdater: it persists a worker's
// display metadata (label / tags / description) to its workers.json
// declaration. Tags are pure WebUI management metadata — the running worker
// ignores them, so this is a declaration edit only, with no live worker
// operation.
type webuiMetaUpdater struct {
	projectID string
}

func (u webuiMetaUpdater) UpdateWorkerMeta(id string, meta webui.WorkerMeta) error {
	if u.projectID == "" {
		return fmt.Errorf("worker metadata update requires a project")
	}
	return UpdateWorkerMeta(u.projectID, id, meta.Label, meta.Tags, meta.Description)
}

// webuiACLUpdater implements webui.WorkerACLUpdater: bus ACL edits are
// persisted into workers.json/workers.archived.json.
type webuiACLUpdater struct{ projectID string }

func (u webuiACLUpdater) UpdateWorkerACL(id string, publish []event.PublishPattern, subscribe []event.EventPattern) error {
	if u.projectID == "" {
		return fmt.Errorf("worker ACL update requires a project")
	}
	return UpdateWorkerACL(u.projectID, id, publish, subscribe)
}

// webuiParamsUpdater implements webui.WorkerParamsUpdater: a params edit lands
// in the workers.json declaration only, so the running worker keeps its current
// definition until it is restarted.
type webuiParamsUpdater struct {
	projectID string
}

func (u webuiParamsUpdater) UpdateWorkerParams(id string, params map[string]any) error {
	if u.projectID == "" {
		return fmt.Errorf("worker params update requires a project")
	}
	return UpdateWorkerParams(u.projectID, id, params)
}
