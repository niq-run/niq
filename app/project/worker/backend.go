// The lifecycle backend: one place that owns every worker's lifecycle,
// whatever its placement. It is what the host worker calls over the bus, and
// what the project assembly calls at startup and shutdown — so a spawn from an
// agent, a start button in the WebUI and a recovery on boot all walk the same
// code path.
package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	projprocess "github.com/niq-run/niq/app/project/worker/process"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/workersvc"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// BackendConfig is everything the lifecycle needs. Every dependency is an
// interface or a value the assembly layer owns; the backend itself decides
// nothing about where the project file or the event bus live.
type BackendConfig struct {
	// ProjectID labels lifecycle log lines when several projects share one
	// process log. Declarations arrive through Decls; nothing else uses it.
	ProjectID string
	// Service builds in-process workers. Required.
	Service *workersvc.WorkerService
	// StateStore persists in-process workers' state/snapshots. It is defined
	// once in workersvc (the lifecycle engine that calls it). Optional: a
	// service configured with its own store (tests) may leave it nil.
	StateStore workersvc.WorkerStore
	// Supervisor supervises managed-process workers. Optional: when nil,
	// placement managed-process is rejected (a project without an HTTP bus has
	// nothing for a child to connect to).
	Supervisor *projprocess.ProcessSupervisor
	// Registry and Signer provision third-party identities. Required for the
	// two process placements.
	Registry corebus.IdentityRegistry
	Signer   *eventbus.TokenSigner
	// Engine is used to drop a destroyed worker's live bus channel (which also
	// broadcasts worker.gone). Optional.
	Engine *eventbus.Engine
	// Decls is the declaration store. Required.
	Decls DeclStore
	// Logf defaults to log.Printf.
	Logf func(string, ...any)
}

// workerHostBackend implements core worker.Backend over an in-process worker
// service, a process supervisor and a declaration store.
type workerHostBackend struct {
	projectID  string
	service    *workersvc.WorkerService
	supervisor *projprocess.ProcessSupervisor
	registry   corebus.IdentityRegistry
	signer     *eventbus.TokenSigner
	engine     *eventbus.Engine
	decls      DeclStore
	logf       func(string, ...any)

	mu        sync.Mutex
	protected map[string]bool
	// locks serializes lifecycle operations per worker id, so a create/destroy
	// race cannot leave two live instances or resurrect a deleted worker.
	locks map[string]*sync.Mutex
}

// NewBackend wires a lifecycle backend. It installs the worker service's spawn
// hook itself: a worker created at runtime records its declaration from the
// config the builder actually produced, which is what makes a runtime spawn
// survive a restart.
func NewBackend(cfg BackendConfig) *workerHostBackend {
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	if cfg.Service != nil && cfg.StateStore != nil {
		cfg.Service.SetStore(cfg.StateStore)
	}
	b := &workerHostBackend{
		projectID:  cfg.ProjectID,
		service:    cfg.Service,
		supervisor: cfg.Supervisor,
		registry:   cfg.Registry,
		signer:     cfg.Signer,
		engine:     cfg.Engine,
		decls:      cfg.Decls,
		logf:       logf,
		protected:  map[string]bool{},
		locks:      map[string]*sync.Mutex{},
	}
	if b.service != nil && b.decls != nil {
		b.service.SetSpawnHook(func(cfg coreworker.WorkerConfig) error {
			d := DeclFromSpec(coreworker.WorkerSpec{WorkerConfig: cfg})
			if _, ok, err := b.decls.Get(d.ID); err != nil || ok {
				return err
			}
			return b.decls.Add(&d)
		})
	}
	return b
}

// idLock returns the per-worker mutex for id.
func (b *workerHostBackend) idLock(id string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.locks[id]
	if !ok {
		l = &sync.Mutex{}
		b.locks[id] = l
	}
	return l
}

// Protected implements worker.Lifecycle: the ids the assembly marked
// essential, plus whatever Recover added to Protect.
func (b *workerHostBackend) Protected(id string) bool { return b.isProtected(id) }

func (b *workerHostBackend) isProtected(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.protected[id]
}

// placementOfSpec resolves a spec's placement, defaulting to in-process.
func placementOfSpec(spec coreworker.WorkerSpec) coreworker.Placement {
	if spec.Placement == "" {
		return coreworker.PlacementInProcess
	}
	return spec.Placement
}

// ── Create ──

// Create brings a worker into existence in the requested placement and records
// its declaration. Validation happens before anything is persisted, so a
// rejected call leaves no trace.
func (b *workerHostBackend) Create(ctx context.Context, spec coreworker.WorkerSpec) (coreworker.WorkerInfo, error) {
	placement := placementOfSpec(spec)
	if err := b.validateCreate(spec, placement); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	if spec.ID != "" {
		l := b.idLock(spec.ID)
		l.Lock()
		defer l.Unlock()
	}
	switch placement {
	case coreworker.PlacementInProcess:
		return b.createInProcess(ctx, spec)
	case coreworker.PlacementManagedProcess:
		return b.createManagedProcess(spec)
	case coreworker.PlacementUnmanagedProcess:
		return b.createUnmanagedProcess(spec)
	}
	return coreworker.WorkerInfo{}, fmt.Errorf("worker: unknown placement %q", placement)
}

func (b *workerHostBackend) validateCreate(spec coreworker.WorkerSpec, placement coreworker.Placement) error {
	if spec.Type == "" {
		return fmt.Errorf("type is required")
	}
	// An explicit id must be safe to use as an identity and a directory name. An
	// empty one is only legal where a builder can derive it (a workspace from its
	// path), so the non-in-process placements — which have no builder — require it.
	if spec.ID == "" {
		if placement != coreworker.PlacementInProcess {
			return fmt.Errorf("id is required for placement %s", placement)
		}
	} else if SanitizeID(spec.ID) != spec.ID {
		return fmt.Errorf("id may only contain letters, digits, '-', '_', '.'")
	}
	if err := ValidateMeta(Declaration{Label: spec.Label, Tags: spec.Tags, Description: spec.Description}); err != nil {
		return err
	}
	if spec.ID != "" {
		if _, ok, err := b.decls.Get(spec.ID); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("worker %s already exists", spec.ID)
		}
		if b.service != nil && b.service.HasWorker(spec.ID) {
			return fmt.Errorf("worker %s already exists", spec.ID)
		}
		if b.registry != nil {
			if _, ok := b.registry.Lookup(spec.ID); ok {
				return fmt.Errorf("worker %s already exists", spec.ID)
			}
		}
	}
	switch placement {
	case coreworker.PlacementInProcess:
		if len(spec.Command) > 0 || spec.Cwd != "" {
			return fmt.Errorf("placement in-process does not take command/cwd")
		}
		return b.service.ValidateSpawn(spec.WorkerConfig)
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("placement managed-process needs a bus: no process supervisor configured")
		}
		if len(spec.Command) == 0 {
			return fmt.Errorf("command is required for placement managed-process")
		}
		if err := validateProcessLaunch(spec); err != nil {
			return err
		}
		return ValidateProcessParams(spec.Params)
	case coreworker.PlacementUnmanagedProcess:
		if len(spec.Command) > 0 {
			return fmt.Errorf("placement unmanaged-process must not carry a command: it starts itself")
		}
		if len(spec.Params) > 0 {
			return fmt.Errorf("placement unmanaged-process cannot carry params: the host has no channel to pass them to a self-started worker")
		}
		if b.registry == nil || b.signer == nil {
			return fmt.Errorf("placement unmanaged-process needs a bus identity registry")
		}
		return nil
	}
	return nil
}

func validateProcessLaunch(spec coreworker.WorkerSpec) error {
	if strings.TrimSpace(spec.Command[0]) == "" {
		return fmt.Errorf("command[0] is empty")
	}
	if spec.Cwd != "" {
		info, err := os.Stat(spec.Cwd)
		if err != nil {
			return fmt.Errorf("cwd %q: %w", spec.Cwd, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("cwd %q is not a directory", spec.Cwd)
		}
	}
	return nil
}

// createInProcess builds a worker in the host process. The declaration is
// written by the service's spawn hook, from the resolved config.
func (b *workerHostBackend) createInProcess(ctx context.Context, spec coreworker.WorkerSpec) (coreworker.WorkerInfo, error) {
	resolved, err := b.service.CreateWorker(ctx, spec.WorkerConfig)
	if err != nil {
		return coreworker.WorkerInfo{}, err
	}
	return coreworker.WorkerInfo{
		ID:        resolved.ID,
		Type:      resolved.Type,
		Placement: coreworker.PlacementInProcess,
		State:     "running",
	}, nil
}

// createManagedProcess declares + provisions + launches an OS process.
func (b *workerHostBackend) createManagedProcess(spec coreworker.WorkerSpec) (coreworker.WorkerInfo, error) {
	d := DeclFromSpec(spec)
	d.Placement = coreworker.PlacementManagedProcess
	if err := b.decls.Add(&d); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	ident := identitySpec(d)
	if err := projprocess.ProvisionManagedProcess(b.registry, b.signer, &ident); err != nil {
		// Declaration stays: the worker shows as declared-but-stopped and a
		// Resume retries the launch.
		return coreworker.WorkerInfo{}, err
	}
	if err := b.supervisor.Start(launchSpec(d, ident.Credential)); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	return coreworker.WorkerInfo{
		ID: d.ID, Type: d.Type, Placement: d.Placement, State: "running",
	}, nil
}

// createUnmanagedProcess declares + provisions a dial-in worker. Nothing is
// launched: the third party connects on its own with the issued credential,
// which is deliberately NOT part of the reply (it is read from the
// declaration by the UI).
func (b *workerHostBackend) createUnmanagedProcess(spec coreworker.WorkerSpec) (coreworker.WorkerInfo, error) {
	d := DeclFromSpec(spec)
	d.Placement = coreworker.PlacementUnmanagedProcess
	if err := b.decls.Add(&d); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	ident := identitySpec(d)
	if err := projprocess.ProvisionUnmanagedProcess(b.registry, b.signer, b.decls, &ident); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	d.Credential = ident.Credential
	return coreworker.WorkerInfo{
		ID: d.ID, Type: d.Type, Placement: d.Placement, State: "stopped",
	}, nil
}

// ── Suspend / Resume / Restart / Destroy ──

// ── Edit ──

// Update applies a partial edit to a worker's declaration. Nothing is
// (re)started: params are construction input, so a running instance follows the
// edit on its next start, and display metadata is cosmetic.
func (b *workerHostBackend) Update(_ context.Context, id string, patch coreworker.WorkerPatch) (coreworker.WorkerInfo, error) {
	l := b.idLock(id)
	l.Lock()
	defer l.Unlock()

	d, err := b.declaration(id)
	if err != nil {
		return coreworker.WorkerInfo{}, err
	}
	next := *d
	if patch.Label != nil {
		next.Label = *patch.Label
	}
	if patch.Tags != nil {
		next.Tags = patch.Tags
	}
	if patch.Description != nil {
		next.Description = *patch.Description
	}
	if patch.Params != nil {
		if len(patch.Params) == 0 {
			next.Params = nil
		} else {
			next.Params = patch.Params
		}
	}
	// Validate the RESULT, not the patch: metadata limits apply to the whole
	// declaration, and params are checked by the placement that consumes them.
	if err := ValidateMeta(next); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	if err := b.validateParams(next); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	if err := b.decls.Update(&next); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	b.logf("[worker %s] updated %s", b.projectID, id)
	return b.infoFor(&next), nil
}

// validateParams checks a declaration's params against whatever consumes them:
// a type's builder for in-process workers, the child environment for
// managed-process, and nothing at all for a self-started worker.
func (b *workerHostBackend) validateParams(d Declaration) error {
	switch PlacementOf(d) {
	case coreworker.PlacementUnmanagedProcess:
		if len(d.Params) > 0 {
			return fmt.Errorf("placement unmanaged-process cannot carry params: the host has no channel to pass them to a self-started worker")
		}
		return nil
	case coreworker.PlacementManagedProcess:
		return ValidateProcessParams(d.Params)
	default:
		if b.service == nil {
			return fmt.Errorf("worker %s: no in-process service to validate params", d.ID)
		}
		return b.service.ValidateSpawn(SpawnConfig(d))
	}
}

// Archive moves a worker in or out of the archived set. Archiving suspends a
// live worker first (so nothing runs behind a hidden declaration) and then
// drops its bus identity; unarchiving restores the declaration and resumes it.
// A dial-in worker has no local process to stop, so archiving only hides it.
func (b *workerHostBackend) Archive(ctx context.Context, id string, archived bool) error {
	l := b.idLock(id)
	l.Lock()
	defer l.Unlock()
	if b.isProtected(id) {
		return fmt.Errorf("worker %s is protected and cannot be archived", id)
	}

	if archived {
		if b.isArchived(id) {
			return nil // already archived: an idempotent no-op
		}
		d, err := b.declaration(id)
		if err != nil {
			return err
		}
		if PlacementOf(*d) != coreworker.PlacementUnmanagedProcess {
			if err := b.suspend(id); err != nil {
				return fmt.Errorf("suspend before archive: %w", err)
			}
		}
		if err := b.decls.Archive(id, true); err != nil {
			return err
		}
		b.dropIdentity(id)
		b.logf("[worker %s] archived %s", b.projectID, id)
		return nil
	}

	if !b.isArchived(id) {
		// Already active (or unknown). Distinguish the two: restoring an active
		// worker is a no-op, restoring an unknown one is a mistake.
		if _, err := b.declaration(id); err != nil {
			return err
		}
		return nil
	}
	if err := b.decls.Archive(id, false); err != nil {
		return err
	}
	// Resume covers every placement: an in-process worker is rebuilt, a
	// managed-process worker is relaunched, a dial-in worker gets its identity
	// and credential re-ensured.
	if err := b.resume(ctx, id); err != nil {
		return fmt.Errorf("restored declaration for %s but resume failed: %w", id, err)
	}
	b.logf("[worker %s] unarchived %s", b.projectID, id)
	return nil
}

// suspend / resume are the placement switchboards shared by the exported
// lifecycle methods and by Archive, which must not re-take the id lock.
func (b *workerHostBackend) suspend(id string) error {
	d, err := b.declaration(id)
	if err != nil {
		if b.service != nil && b.service.HasWorker(id) {
			return b.service.SuspendWorker(id)
		}
		return err
	}
	switch PlacementOf(*d) {
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("worker %s: no process supervisor", id)
		}
		return b.supervisor.Stop(id)
	case coreworker.PlacementUnmanagedProcess:
		return nil // nothing local to stop
	default:
		if b.service == nil || !b.service.HasWorker(id) {
			return nil // declared but not instantiated
		}
		return b.service.SuspendWorker(id)
	}
}

func (b *workerHostBackend) resume(ctx context.Context, id string) error {
	d, err := b.declaration(id)
	if err != nil {
		return err
	}
	switch PlacementOf(*d) {
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("worker %s: no process supervisor", id)
		}
		ident := identitySpec(*d)
		if err := projprocess.ProvisionManagedProcess(b.registry, b.signer, &ident); err != nil {
			return err
		}
		return b.supervisor.Start(launchSpec(*d, ident.Credential))
	case coreworker.PlacementUnmanagedProcess:
		ident := identitySpec(*d)
		return projprocess.ProvisionUnmanagedProcess(b.registry, b.signer, b.decls, &ident)
	default:
		if b.service == nil {
			return fmt.Errorf("worker %s: no in-process service", id)
		}
		if b.service.HasWorker(id) {
			return b.service.ResumeWorker(ctx, id)
		}
		_, err := b.service.CreateWorker(ctx, SpawnConfig(*d))
		return err
	}
}

// dropIdentity removes a worker's bus identity, if it has one.
func (b *workerHostBackend) dropIdentity(id string) {
	if b.engine != nil && b.engine.Channel(id) != nil {
		b.engine.Disconnect(id) // also broadcasts worker.gone
	}
	if b.registry == nil {
		return
	}
	if _, ok := b.registry.Lookup(id); ok {
		if err := b.registry.Revoke(id); err != nil {
			b.logf("[worker %s] revoke %s: %v", b.projectID, id, err)
		}
	}
}

func (b *workerHostBackend) declaration(id string) (*Declaration, error) {
	d, ok, err := b.decls.Get(id)
	if err != nil {
		return nil, err
	}
	if ok {
		return d, nil
	}
	return nil, fmt.Errorf("worker %s is not declared", id)
}

func (b *workerHostBackend) Suspend(ctx context.Context, id string) error {
	l := b.idLock(id)
	l.Lock()
	defer l.Unlock()
	if b.isProtected(id) {
		return fmt.Errorf("worker %s is protected and cannot be suspended", id)
	}
	d, err := b.declaration(id)
	if err != nil {
		// A live worker without a declaration (transient state) still obeys its
		// owner.
		if b.service != nil && b.service.HasWorker(id) {
			return b.service.SuspendWorker(id)
		}
		return err
	}
	switch PlacementOf(*d) {
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("worker %s: no process supervisor", id)
		}
		return b.supervisor.Stop(id)
	case coreworker.PlacementUnmanagedProcess:
		return fmt.Errorf("worker %s runs outside this host and cannot be suspended", id)
	default:
		return b.service.SuspendWorker(id)
	}
}

func (b *workerHostBackend) Resume(ctx context.Context, id string) error {
	l := b.idLock(id)
	l.Lock()
	defer l.Unlock()
	d, err := b.declaration(id)
	if err != nil {
		return err
	}
	switch PlacementOf(*d) {
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("worker %s: no process supervisor", id)
		}
		ident := identitySpec(*d)
		if err := projprocess.ProvisionManagedProcess(b.registry, b.signer, &ident); err != nil {
			return err
		}
		return b.supervisor.Start(launchSpec(*d, ident.Credential))
	case coreworker.PlacementUnmanagedProcess:
		ident := identitySpec(*d)
		if err := projprocess.ProvisionUnmanagedProcess(b.registry, b.signer, b.decls, &ident); err != nil {
			return err
		}
		d.Credential = ident.Credential
		return nil
	default:
		if b.service.HasWorker(id) {
			return b.service.ResumeWorker(ctx, id)
		}
		_, err := b.service.CreateWorker(ctx, SpawnConfig(*d))
		return err
	}
}

func (b *workerHostBackend) Restart(ctx context.Context, id string) error {
	l := b.idLock(id)
	l.Lock()
	defer l.Unlock()
	if b.isProtected(id) {
		return fmt.Errorf("worker %s is protected and cannot be restarted", id)
	}
	d, err := b.declaration(id)
	if err != nil {
		return err
	}
	switch PlacementOf(*d) {
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("worker %s: no process supervisor", id)
		}
		if err := b.supervisor.Remove(id); err != nil {
			return err
		}
		ident := identitySpec(*d)
		if err := projprocess.ProvisionManagedProcess(b.registry, b.signer, &ident); err != nil {
			return err
		}
		return b.supervisor.Start(launchSpec(*d, ident.Credential))
	case coreworker.PlacementUnmanagedProcess:
		return fmt.Errorf("worker %s runs outside this host and cannot be restarted", id)
	default:
		return b.service.RestartWorker(ctx, SpawnConfig(*d))
	}
}

func (b *workerHostBackend) Destroy(ctx context.Context, id string) error {
	l := b.idLock(id)
	l.Lock()
	defer l.Unlock()
	if b.isProtected(id) {
		return fmt.Errorf("worker %s is protected and cannot be destroyed", id)
	}

	// Stop the live side first and let it delete the worker's runtime state, so
	// nothing is still writing into a directory being removed. The live side
	// owns that state (the service owns state.json, the supervisor owns the
	// process log and the child's state dir); the declaration store does not.
	if b.service != nil && b.service.HasWorker(id) {
		if err := b.service.DestroyWorker(id); err != nil {
			return err
		}
	} else if b.supervisor != nil {
		// Also covers an in-process worker that is not live in this run: its
		// state directory is under the same <workers>/<id> layout.
		if err := b.supervisor.Destroy(id); err != nil {
			return err
		}
	}

	// Drop the declaration, then the bus side.
	if err := b.decls.Delete(id); err != nil {
		return err
	}
	if b.engine != nil && b.engine.Channel(id) != nil {
		b.engine.Disconnect(id) // also broadcasts worker.gone
	}
	if b.registry != nil {
		if _, ok := b.registry.Lookup(id); ok {
			if err := b.registry.Revoke(id); err != nil {
				return err
			}
		}
	}
	b.logf("[worker %s] destroyed %s", b.projectID, id)
	return nil
}

// ── Catalog ──

func (b *workerHostBackend) infoFor(d *Declaration) coreworker.WorkerInfo {
	vi := coreworker.WorkerInfo{ID: d.ID, Type: d.Type, Placement: PlacementOf(*d), State: "stopped"}
	switch vi.Placement {
	case coreworker.PlacementManagedProcess:
		if b.supervisor != nil {
			if st, ok := b.supervisor.Get(d.ID); ok {
				vi.State = st.State
			}
		}
	case coreworker.PlacementUnmanagedProcess:
		// Nothing local to report: it is "stopped" from this host's point of
		// view (whether it is connected is the registry's business).
	default:
		if b.service != nil {
			for _, wi := range b.service.ListWorkers("") {
				if wi.ID == d.ID {
					vi.State = string(wi.State)
				}
			}
		}
	}
	return vi
}

// Get returns one worker's lifecycle view.
func (b *workerHostBackend) Get(id string) (coreworker.WorkerInfo, bool, error) {
	d, ok, err := b.decls.Get(id)
	if err != nil {
		return coreworker.WorkerInfo{}, false, err
	}
	if ok {
		return b.infoFor(d), true, nil
	}
	// Not declared means not part of the ACTIVE set. An archived worker may still
	// have a suspended entry here (archiving keeps its snapshot so a restore can
	// resume where it stopped); it must not resurface in the active catalog.
	if b.isArchived(id) {
		return coreworker.WorkerInfo{}, false, nil
	}
	// A live worker that has no declaration yet (a spawn in flight).
	if b.service != nil {
		for _, wi := range b.service.ListWorkers("") {
			if wi.ID == id {
				return coreworker.WorkerInfo{ID: wi.ID, Type: wi.Type, Placement: coreworker.PlacementInProcess, State: string(wi.State)}, true, nil
			}
		}
	}
	if b.supervisor != nil {
		if st, ok := b.supervisor.Get(id); ok {
			return coreworker.WorkerInfo{ID: st.ID, Type: st.Type, Placement: coreworker.PlacementManagedProcess, State: st.State}, true, nil
		}
	}
	return coreworker.WorkerInfo{}, false, nil
}

// List returns every declared worker plus any live worker not (yet) declared.
func (b *workerHostBackend) List() []coreworker.WorkerInfo {
	decls, err := b.decls.List()
	if err != nil {
		b.logf("[worker %s] list declarations: %v", b.projectID, err)
	}
	seen := make(map[string]bool, len(decls))
	out := make([]coreworker.WorkerInfo, 0, len(decls))
	for _, d := range decls {
		seen[d.ID] = true
		out = append(out, b.infoFor(d))
	}
	if b.service != nil {
		for _, wi := range b.service.ListWorkers("") {
			if seen[wi.ID] {
				continue
			}
			out = append(out, coreworker.WorkerInfo{ID: wi.ID, Type: wi.Type, Placement: coreworker.PlacementInProcess, State: string(wi.State)})
		}
	}
	if b.supervisor != nil {
		for _, st := range b.supervisor.List() {
			if seen[st.ID] {
				continue
			}
			out = append(out, coreworker.WorkerInfo{ID: st.ID, Type: st.Type, Placement: coreworker.PlacementManagedProcess, State: st.State})
		}
	}
	return out
}

// isArchived reports whether id is in the archived file. It costs one file read,
// so it is only called on a miss: the case where the answer would otherwise be
// wrong.
func (b *workerHostBackend) isArchived(id string) bool {
	ids, err := b.decls.Archived()
	if err != nil {
		b.logf("[worker %s] list archived: %v", b.projectID, err)
		return false
	}
	for _, a := range ids {
		if a == id {
			return true
		}
	}
	return false
}

// Archived returns the ids held in the archived file. It is what the WebUI's
// archive panel lists: declared and restorable, but not part of the active set.
func (b *workerHostBackend) Archived() []string {
	ids, err := b.decls.Archived()
	if err != nil {
		b.logf("[worker %s] list archived: %v", b.projectID, err)
		return nil
	}
	return ids
}

// SeedRemoteIdentities registers the active unmanaged-process workers in the
// in-memory identity registry before the bus starts serving remote connections.
// Their credential is persisted in workers.json; an empty one is minted here.
func (b *workerHostBackend) SeedRemoteIdentities(ctx context.Context) error {
	decls, err := b.decls.List()
	if err != nil {
		return err
	}
	for _, d := range decls {
		if PlacementOf(*d) != coreworker.PlacementUnmanagedProcess {
			continue
		}
		ident := identitySpec(*d)
		if err := projprocess.ProvisionUnmanagedProcess(b.registry, b.signer, b.decls, &ident); err != nil {
			return fmt.Errorf("seed %s: %w", d.ID, err)
		}
	}
	return nil
}

// ── Recover / Run ──

// Recover materializes the declared set: in-process workers through the worker
// service (with its snapshot/store machinery), process workers through their
// own start path. Protect/Essential are handed to the service and mirrored in
// the backend, so a protected id is refused for every placement.
func (b *workerHostBackend) Recover(ctx context.Context, opts coreworker.RecoverOptions) error {
	b.mu.Lock()
	for _, id := range opts.Protect {
		b.protected[id] = true
	}
	for _, spec := range opts.Essential {
		b.protected[spec.ID] = true
	}
	b.mu.Unlock()

	inProc := func(specs []coreworker.WorkerSpec) []coreworker.WorkerConfig {
		out := make([]coreworker.WorkerConfig, 0, len(specs))
		for _, s := range specs {
			if placementOfSpec(s) == coreworker.PlacementInProcess {
				out = append(out, s.WorkerConfig)
			}
		}
		return out
	}

	// The declaration store is authoritative for the declared set; the specs
	// handed in are the ones the caller built from it. Process workers are
	// started after the in-process set, so the bus-facing control plane exists
	// first.
	if err := b.service.RecoverAll(ctx, workersvc.RecoverOptions{
		Workers:   inProc(opts.Declared),
		Essential: inProc(opts.Essential),
		Suspend:   opts.Suspend,
		Protect:   opts.Protect,
	}); err != nil {
		return err
	}

	essential := make(map[string]bool, len(opts.Essential))
	for _, s := range opts.Essential {
		essential[s.ID] = true
	}
	suspended := make(map[string]bool, len(opts.Suspend))
	for _, id := range opts.Suspend {
		suspended[id] = true
	}

	// One pass over essential ∪ declared: an essential worker is usually also
	// declared, and resuming it twice would restart the child it just launched.
	seen := make(map[string]bool, len(opts.Declared)+len(opts.Essential))
	for _, spec := range append(append([]coreworker.WorkerSpec{}, opts.Essential...), opts.Declared...) {
		if placementOfSpec(spec) == coreworker.PlacementInProcess {
			continue
		}
		if seen[spec.ID] {
			continue
		}
		seen[spec.ID] = true
		if !essential[spec.ID] && suspended[spec.ID] {
			continue
		}
		if placementOfSpec(spec) == coreworker.PlacementManagedProcess && len(spec.Command) == 0 {
			b.logf("[worker %s] managed-process %s: empty command, skipping", b.projectID, spec.ID)
			continue
		}
		if err := b.Resume(ctx, spec.ID); err != nil {
			b.logf("[worker %s] resume %s: %v", b.projectID, spec.ID, err)
		}
	}
	return nil
}

// Run blocks until ctx is done, then releases every worker it owns.
func (b *workerHostBackend) Run(ctx context.Context) error {
	err := b.service.Run(ctx)
	if b.supervisor != nil {
		b.supervisor.Shutdown()
	}
	return err
}

// ── Declaration → process package inputs ──

// identitySpec lowers a declaration's ACL into the provisioning input. The
// declaration's typed SubscriptionSpec/PublishSpec become bus patterns here so
// the process package needs no dependency on the declaration model.
func identitySpec(d Declaration) projprocess.IdentitySpec {
	subs := make([]event.EventPattern, 0, len(d.Subscriptions))
	for _, s := range d.Subscriptions {
		subs = append(subs, s.ToPattern())
	}
	pubs := make([]event.PublishPattern, 0, len(d.Publish))
	for _, p := range d.Publish {
		pubs = append(pubs, p.ToPublishPattern())
	}
	return projprocess.IdentitySpec{
		ID:              d.ID,
		Type:            d.Type,
		Credential:      d.Credential,
		Subscriptions:   subs,
		Publish:         pubs,
		TokenTTLSeconds: d.TokenTTLSeconds,
	}
}

// launchSpec lowers a declaration into the supervisor's launch input. The
// credential is the freshly minted one (managed-process never persists it).
func launchSpec(d Declaration, credential string) projprocess.Spec {
	env := make(map[string]string, len(d.Params))
	for k, v := range d.Params {
		if s, ok := v.(string); ok {
			env[k] = projprocess.ResolveSecret(s)
		}
	}
	return projprocess.Spec{
		ID:         d.ID,
		Type:       d.Type,
		Command:    d.Command,
		Env:        env,
		Cwd:        d.Cwd,
		Credential: credential,
	}
}
