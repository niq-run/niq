// ControlPlane is the complete worker control plane for the standalone
// deployment form: it owns every worker's lifecycle, its manifest, and the
// state of the app-hosted interface worker, and it exposes stable exits for the
// host worker and the WebUI.
//
// It is self-closed. The assembly injects storage providers once; from then on
// recovery reads the manifest store itself, shutdown is an explicit Stop, and
// nothing outside decides which workers exist.
package worker

import (
	"context"
	"fmt"
	"log"

	"github.com/niq-run/niq/app/standalone/worker/builtin"
	"github.com/niq-run/niq/app/standalone/worker/process"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/workerctl"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// Config is everything the control plane needs from the deployment form. Every
// storage boundary is an interface the project implements; every mechanism
// (instance manager, process supervisor) is a library this package assembles.
type Config struct {
	ProjectID  string
	Manifests  ManifestStore
	StateStore workerctl.WorkerStore
	Registry   corebus.IdentityRegistry
	Bus        corebus.Control
	Signer     *eventbus.TokenSigner
	Engine     *eventbus.Engine
	Supervisor *process.Supervisor
	// Protect lists ids that may never be suspended, restarted, archived or
	// destroyed (the host, the WebUI's interface worker).
	Protect []string
	Logf    func(string, ...any)
}

// ControlPlane owns the worker subsystem for one project.
type ControlPlane struct {
	backend    *workerHostBackend
	manager    *workerctl.Manager
	types      *builtin.Registry
	manifests  ManifestStore
	state      workerctl.WorkerStore
	supervisor *process.Supervisor
	protect    []string
	logf       func(string, ...any)
}

// New wires a control plane. It installs the manager's spawn hook: a worker
// created at runtime records its manifest from the config the builder actually
// produced, which is what makes a runtime spawn survive a restart.
func New(cfg Config) *ControlPlane {
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	types := builtin.NewRegistry()
	manager := workerctl.New(workerctl.Config{Store: cfg.StateStore, Build: types.Build})
	cp := &ControlPlane{
		manifests:  cfg.Manifests,
		state:      cfg.StateStore,
		supervisor: cfg.Supervisor,
		protect:    cfg.Protect,
		logf:       logf,
		types:      types,
		manager:    manager,
	}
	cp.backend = newBackend(backendConfig{
		ProjectID:  cfg.ProjectID,
		Manager:    manager,
		Types:      types,
		Manifests:  cfg.Manifests,
		Supervisor: cfg.Supervisor,
		Registry:   cfg.Registry,
		Signer:     cfg.Signer,
		Bus:        cfg.Bus,
		Engine:     cfg.Engine,
		Logf:       logf,
	})
	manager.SetSpawnHook(func(conf coreworker.WorkerConfig) error {
		m := cp.backend.manifestFromSpec(coreworker.WorkerSpec{WorkerConfig: conf})
		if _, ok, err := cp.manifests.Get(m.ID); err != nil || ok {
			return err
		}
		return cp.manifests.Add(&m)
	})
	return cp
}

// RegisterBuilders registers one builder per known worker type.
func (c *ControlPlane) RegisterBuilders(ctx builtin.BuildContext) { builtin.Register(ctx, c.types) }

// RegisterBuilder registers a single type. It is the seam tests and
// out-of-tree worker types use; the built-in set goes through RegisterBuilders.
func (c *ControlPlane) RegisterBuilder(typ string, b workerctl.Builder, check func(map[string]any) error) {
	c.types.RegisterBuilder(typ, b, check)
}

// Types returns the registered worker type labels, sorted.
func (c *ControlPlane) Types() []string { return c.types.Types() }

// Manager exposes the instance manager to the assembly: builders use it to
// checkpoint a worker whose durable state changed at runtime.
func (c *ControlPlane) Manager() *workerctl.Manager { return c.manager }

// HostBackend is the exit the host worker drives: the full lifecycle plus
// manifest surface, with no ACL editing (identity writes belong to the bus
// control plane).
func (c *ControlPlane) HostBackend() coreworker.Backend { return c.backend }

// Manifest is the exit a display layer consumes: Get / List / Update over the
// unified WorkerInfo view.
func (c *ControlPlane) Manifest() coreworker.Manifest { return c.backend }

// Recover restores the whole active manifest set. In-process workers are
// recovered first (through the instance manager and their persisted snapshots),
// then process workers are resumed, so the bus-facing workers exist before a
// child connects.
func (c *ControlPlane) Recover(ctx context.Context) error {
	c.backend.markProtected(c.protect)

	manifests, err := c.manifests.List(false)
	if err != nil {
		return fmt.Errorf("recover: list manifests: %w", err)
	}
	inProc := make([]coreworker.WorkerConfig, 0, len(manifests))
	for _, m := range manifests {
		if PlacementOf(*m) == coreworker.PlacementInProcess {
			inProc = append(inProc, SpawnConfig(*m))
		}
	}
	if err := c.manager.RecoverAll(ctx, inProc, c.protect); err != nil {
		return err
	}
	for _, m := range manifests {
		if PlacementOf(*m) == coreworker.PlacementInProcess {
			continue
		}
		if PlacementOf(*m) == coreworker.PlacementManagedProcess && len(m.Command) == 0 {
			c.logf("[worker %s] managed-process %s: empty command, skipping", c.backend.projectID, m.ID)
			continue
		}
		if err := c.backend.Resume(ctx, m.ID); err != nil {
			c.logf("[worker %s] resume %s: %v", c.backend.projectID, m.ID, err)
		}
	}
	return nil
}

// Stop releases everything the control plane owns: snapshot live workers while
// they are still alive, stop the in-process ones, then reap third-party
// processes. It never blocks waiting for a signal — process liveness belongs to
// the assembly.
func (c *ControlPlane) Stop(context.Context) error {
	c.manager.SnapshotAll()
	c.manager.StopAll()
	if c.supervisor != nil {
		c.supervisor.Shutdown()
	}
	return nil
}

// SaveState persists an app-hosted worker's snapshot (the WebUI's approval
// inbox goes through here). An empty snapshot is a no-op, not a state write.
func (c *ControlPlane) SaveState(id string, snapshot []byte) error {
	if c.state == nil || len(snapshot) == 0 {
		return nil
	}
	return c.state.SaveState(id, coreworker.StateRunning, snapshot)
}

// State returns the persisted snapshot for an app-hosted worker.
func (c *ControlPlane) State(id string) ([]byte, bool, error) {
	if c.state == nil {
		return nil, false, nil
	}
	recs, err := c.state.LoadAll()
	if err != nil {
		return nil, false, fmt.Errorf("load state: %w", err)
	}
	for _, rec := range recs {
		if rec.ID == id && len(rec.Snapshot) > 0 {
			return rec.Snapshot, true, nil
		}
	}
	return nil, false, nil
}
