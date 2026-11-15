// WebUI ↔ project bridge: the read-only projections and input translators the
// project WebUI server is wired with.
//
// The WebUI never writes anything itself. Every worker WRITE is a bus call to
// the host worker (spawn / suspend / resume / restart / destroy / update /
// archive), and every bus ACL write goes to the identity administrator, which is
// the bus control plane. What remains here is (a) live READ projections of the
// declarations (labels, tags, params) and (b) a pure translation of the create
// form into the host's spawn arguments.
package project

import (
	"encoding/json"
	"fmt"

	projworker "github.com/niq-run/niq/app/project/worker"
	"github.com/niq-run/niq/app/webui"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// webuiDecls reads the project's declarations live, for WorkerDeclProvider. It
// re-reads on every call, so an edit made through the host (or a worker created
// or destroyed at runtime) is visible on the next render.
type webuiDecls struct{ projectID string }

func (d webuiDecls) WorkerDecls() map[string]webui.WorkerDecl {
	decls, err := LoadAllWorkerDecls(d.projectID)
	if err != nil {
		return nil
	}
	out := make(map[string]webui.WorkerDecl, len(decls))
	for id, decl := range decls {
		out[id] = webui.WorkerDecl{
			Label:       decl.Label,
			Tags:        decl.Tags,
			Description: decl.Description,
			Params:      decl.Params,
		}
	}
	return out
}

// identityAdmin is the project's bus control plane: the durable half of a bus
// identity (ACL + credential) lives in the worker's declaration, and the runtime
// registry is its projection. An ACL edit therefore spans two layers, and this
// is the ONE place that owns that span — persist first (workers.json is the
// source), then apply to the registry.
//
// It is deliberately not part of worker.Backend: publish/subscribe grants are bus
// identity data (core/itfs/bus calls its registry the control plane), not part of
// a worker's lifecycle.
type identityAdmin struct {
	projectID string
	registry  corebus.IdentityRegistry
}

// UpdateACL persists a worker's allow lists and applies them to the live
// registry, in that order: a crash between the two leaves an on-disk grant the
// next start re-applies, never a live grant that would vanish.
func (a identityAdmin) UpdateACL(id string, publish []event.PublishPattern, subscribe []event.EventPattern) error {
	if a.projectID == "" {
		return fmt.Errorf("ACL update requires a project")
	}
	if err := UpdateWorkerACL(a.projectID, id, publish, subscribe); err != nil {
		return err
	}
	if a.registry == nil {
		return nil
	}
	if _, ok := a.registry.Lookup(id); !ok {
		return nil // declared but no identity yet: registration picks the grant up
	}
	return a.registry.Update(id, publish, subscribe)
}

// webuiSpawnArgs turns the create-form body into the host worker's `spawn`
// arguments. The form nests type-specific params under "params" while spawn takes
// them as top-level properties, so this flattens them; everything else (type,
// id, placement, command, cwd, display metadata, ACL) is already spawn-shaped.
// Nothing is validated or persisted here — the host does both.
type webuiSpawnArgs struct{ projectID string }

func (a webuiSpawnArgs) SpawnArgs(body json.RawMessage) (map[string]any, error) {
	var form map[string]any
	if err := json.Unmarshal(body, &form); err != nil {
		return nil, fmt.Errorf("bad worker definition: %w", err)
	}
	if a.projectID != "" {
		form = projworker.InstantiateForm(form, a.projectID)
	}
	args := make(map[string]any, len(form)+4)
	for k, v := range form {
		args[k] = v
	}
	params, ok := args["params"].(map[string]any)
	if !ok {
		return args, nil
	}
	delete(args, "params")
	for k, v := range params {
		if _, taken := args[k]; taken {
			return nil, fmt.Errorf("param %q collides with the \"%s\" launch field", k, k)
		}
		args[k] = v
	}
	return args, nil
}
