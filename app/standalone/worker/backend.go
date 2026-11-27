// The worker lifecycle backend: one place that owns every worker's lifecycle,
// whatever its placement. It is what the host worker calls over the bus, and
// what the control plane drives at startup and shutdown — so a spawn from an
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

	"github.com/niq-run/niq/app/standalone/vault"
	"github.com/niq-run/niq/app/standalone/worker/builtin"
	"github.com/niq-run/niq/app/standalone/worker/process"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/workerctl"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// backendConfig is everything the lifecycle needs. Every dependency is an
// interface or a value the assembly layer owns; the backend itself decides
// nothing about where the project file or the event bus live.
type backendConfig struct {
	// ProjectID labels lifecycle log lines when several projects share one
	// process log.
	ProjectID string
	// Manager runs live worker instances. Required.
	Manager *workerctl.Manager
	// Types resolves worker type → builder. Required.
	Types *builtin.Registry
	// Manifests is the manifest store. Required.
	Manifests ManifestStore
	// Supervisor supervises managed-process workers. Optional: when nil,
	// placement managed-process is rejected (a project without an HTTP bus has
	// nothing for a child to connect to).
	Supervisor *process.Supervisor
	// Registry and Signer provision third-party identities. Required for the
	// two process placements.
	Registry corebus.IdentityRegistry
	Signer   *eventbus.TokenSigner
	// Bus is the durable identity control plane. Required.
	Bus corebus.Control
	// Engine is used to drop a destroyed worker's live bus channel (which also
	// broadcasts worker.gone). Optional.
	Engine *eventbus.Engine
	// Logf defaults to log.Printf.
	Logf func(string, ...any)
	// Secrets resolves `env:` / `file:` (and, later, `vault:`) references when a
	// managed process's params become its environment. Optional: nil means the
	// default env/file provider.
	Secrets vault.Provider
}

// workerHostBackend implements core worker.Backend over the instance manager,
// the process supervisor, the manifest store and the bus control plane.
type workerHostBackend struct {
	projectID  string
	manager    *workerctl.Manager
	types      *builtin.Registry
	manifests  ManifestStore
	supervisor *process.Supervisor
	registry   corebus.IdentityRegistry
	bus        corebus.Control
	signer     *eventbus.TokenSigner
	engine     *eventbus.Engine
	logf       func(string, ...any)
	secrets    vault.Provider

	mu        sync.Mutex
	protected map[string]bool
	// locks serializes lifecycle operations per worker id, so a create/destroy
	// race cannot leave two live instances or resurrect a deleted worker.
	locks map[string]*sync.Mutex
}

// newBackend wires a lifecycle backend.
func newBackend(cfg backendConfig) *workerHostBackend {
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	secrets := cfg.Secrets
	if secrets == nil {
		secrets = vault.Fallback{}
	}
	return &workerHostBackend{
		projectID:  cfg.ProjectID,
		manager:    cfg.Manager,
		types:      cfg.Types,
		manifests:  cfg.Manifests,
		supervisor: cfg.Supervisor,
		registry:   cfg.Registry,
		bus:        cfg.Bus,
		signer:     cfg.Signer,
		engine:     cfg.Engine,
		logf:       logf,
		secrets:    secrets,
		protected:  map[string]bool{},
		locks:      map[string]*sync.Mutex{},
	}
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

// markProtected records the ids that may never be suspended, restarted,
// archived or destroyed.
func (b *workerHostBackend) markProtected(ids []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range ids {
		b.protected[id] = true
	}
}

// Protected implements worker.Lifecycle.
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
// its manifest. Validation happens before anything is persisted, so a rejected
// call leaves no trace.
func (b *workerHostBackend) Create(ctx context.Context, spec coreworker.WorkerSpec) (coreworker.WorkerInfo, error) {
	placement := placementOfSpec(spec)
	if err := b.validateCreate(spec, placement); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	// A credential-bearing param is sealed before anything is written or built:
	// the vault gets the value, the declaration gets a `vault:` reference, and a
	// ref the caller already used is left alone.
	sealed, err := b.sealParams(spec.Type, spec.ID, spec.Params)
	if err != nil {
		return coreworker.WorkerInfo{}, err
	}
	spec.Params = sealed
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
	if err := ValidateMeta(Manifest{Label: spec.Label, Tags: spec.Tags, Description: spec.Description}); err != nil {
		return err
	}
	if spec.ID != "" {
		if _, ok, err := b.manifests.Get(spec.ID); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("worker %s already exists", spec.ID)
		}
		if b.manager != nil && b.manager.Has(spec.ID) {
			return fmt.Errorf("worker %s already exists", spec.ID)
		}
		if b.bus != nil {
			if _, ok := b.bus.Lookup(spec.ID); ok {
				return fmt.Errorf("worker %s already exists", spec.ID)
			}
		}
	}
	switch placement {
	case coreworker.PlacementInProcess:
		if len(spec.Command) > 0 || spec.Cwd != "" {
			return fmt.Errorf("placement in-process does not take command/cwd")
		}
		return b.types.Validate(spec.WorkerConfig)
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
		return builtin.ValidateProcessParams(spec.Params)
	case coreworker.PlacementUnmanagedProcess:
		if len(spec.Command) > 0 {
			return fmt.Errorf("placement unmanaged-process must not carry a command: it starts itself")
		}
		if len(spec.Params) > 0 {
			return fmt.Errorf("placement unmanaged-process cannot carry params: the host has no channel to pass them to a self-started worker")
		}
		if b.signer == nil || b.bus == nil {
			return fmt.Errorf("placement unmanaged-process needs a bus identity control plane")
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

// createInProcess builds a worker in the host process. The manifest is written
// by the manager's spawn hook, from the resolved config.
func (b *workerHostBackend) createInProcess(ctx context.Context, spec coreworker.WorkerSpec) (coreworker.WorkerInfo, error) {
	resolved, err := b.manager.Create(ctx, spec.WorkerConfig)
	if err != nil {
		return coreworker.WorkerInfo{}, err
	}
	return coreworker.WorkerInfo{
		ID:          resolved.ID,
		Type:        resolved.Type,
		Placement:   coreworker.PlacementInProcess,
		State:       "running",
		Label:       resolved.Label,
		Tags:        resolved.Tags,
		Description: resolved.Description,
		Params:      resolved.Params,
	}, nil
}

// createManagedProcess declares + provisions + launches an OS process.
func (b *workerHostBackend) createManagedProcess(spec coreworker.WorkerSpec) (coreworker.WorkerInfo, error) {
	m := b.manifestFromSpec(spec)
	m.Placement = coreworker.PlacementManagedProcess
	if err := b.manifests.Add(&m); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	credential, err := process.ProvisionManagedProcess(b.registry, b.signer, b.bus, identityFromSpec(spec))
	if err != nil {
		// The manifest stays: the worker shows as declared-but-stopped and a
		// Resume retries the launch.
		return coreworker.WorkerInfo{}, err
	}
	if err := b.supervisor.Start(b.launchSpec(m, credential)); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	return b.infoFor(&m), nil
}

// createUnmanagedProcess declares + provisions a dial-in worker. Nothing is
// launched: the third party connects on its own with the issued credential,
// which is deliberately NOT part of the reply (it is read from the identity by
// the UI).
func (b *workerHostBackend) createUnmanagedProcess(spec coreworker.WorkerSpec) (coreworker.WorkerInfo, error) {
	m := b.manifestFromSpec(spec)
	m.Placement = coreworker.PlacementUnmanagedProcess
	if err := b.manifests.Add(&m); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	if _, err := process.ProvisionUnmanagedProcess(b.bus, b.signer, identityFromSpec(spec)); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	return b.infoFor(&m), nil
}

// Update applies a partial edit to a worker's manifest. Nothing is (re)started:
// params are construction input, so a running instance follows the edit on its
// next start, and display metadata is cosmetic.
//
// `params` MERGES: only the given keys change, and a nil value removes that key
// (JSON null clears). A call that repoints one field therefore cannot clobber
// the rest by omission, which is what makes a self-service edit (a niq worker
// repointing its link) safe against a concurrent edit made elsewhere.
func (b *workerHostBackend) Update(_ context.Context, id string, patch coreworker.WorkerPatch) (coreworker.WorkerInfo, error) {
	l := b.idLock(id)
	l.Lock()
	defer l.Unlock()

	m, err := b.manifest(id)
	if err != nil {
		return coreworker.WorkerInfo{}, err
	}
	next := *m
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
		merged := make(map[string]any, len(m.Params)+len(patch.Params))
		for k, v := range m.Params {
			merged[k] = v
		}
		for k, v := range patch.Params {
			if v == nil {
				delete(merged, k)
				continue
			}
			merged[k] = v
		}
		if len(merged) == 0 {
			next.Params = nil
		} else {
			next.Params = merged
		}
	}
	// Validate the RESULT, not the patch: metadata limits apply to the whole
	// manifest, and params are checked by the placement that consumes them.
	if err := ValidateMeta(next); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	if err := b.validateParams(next); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	sealed, err := b.sealParams(next.Type, next.ID, next.Params)
	if err != nil {
		return coreworker.WorkerInfo{}, err
	}
	next.Params = sealed
	if err := b.manifests.Update(&next); err != nil {
		return coreworker.WorkerInfo{}, err
	}
	b.logf("[worker %s] updated %s", b.projectID, id)
	return b.infoFor(&next), nil
}

// manifestFromSpec lowers a spec into a manifest for THIS project, resolving
// the template-layer "{project}" placeholder. Every manifest the backend writes
// goes through it, so a worker created from a form and a worker seeded from a
// template end up with identical, fully-resolved params.
func (b *workerHostBackend) manifestFromSpec(spec coreworker.WorkerSpec) Manifest {
	return Instantiate(ManifestFromSpec(spec), b.projectID)
}

// validateParams checks a manifest's params against whatever consumes them: a
// type's builder for in-process workers, the child environment for
// managed-process, and nothing at all for a self-started worker.
func (b *workerHostBackend) validateParams(m Manifest) error {
	switch PlacementOf(m) {
	case coreworker.PlacementUnmanagedProcess:
		if len(m.Params) > 0 {
			return fmt.Errorf("placement unmanaged-process cannot carry params: the host has no channel to pass them to a self-started worker")
		}
		return nil
	case coreworker.PlacementManagedProcess:
		return builtin.ValidateProcessParams(m.Params)
	default:
		if b.types == nil {
			return fmt.Errorf("worker %s: no type registry to validate params", m.ID)
		}
		return b.types.Validate(SpawnConfig(m))
	}
}

// sealParams replaces literal credentials with vault references.
//
// Which params are credentials is the TYPE's declaration (registry.MarkSecret),
// not a guess about a name: the write path cannot know what a param means.
//
//   - an empty value or an existing reference (`vault:`/`env:`/`file:`) is left
//     alone, so re-saving never re-seals and never overwrites a deliberate ref;
//   - a non-string value is an error, not a silent stringification;
//   - a type with secret params needs an explicit id, because the entry name is
//     derived from it.
//
// A host whose provider cannot write (the env/file fallback, used by tests and
// embedded hosts) leaves values untouched: sealing needs somewhere to put them.
func (b *workerHostBackend) sealParams(typ, id string, params map[string]any) (map[string]any, error) {
	if params == nil || b.types == nil {
		return params, nil
	}
	names := b.types.SecretParams(typ)
	if len(names) == 0 {
		return params, nil
	}
	storer, ok := b.secrets.(interface {
		Put(name, value string) error
	})
	if !ok {
		return params, nil
	}

	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	for _, key := range names {
		raw, present := out[key]
		if !present {
			continue
		}
		s, isString := raw.(string)
		if !isString {
			return nil, fmt.Errorf("worker %s: param %q carries a credential and must be a string", id, key)
		}
		if s == "" || vault.IsRef(s) {
			continue
		}
		if id == "" {
			return nil, fmt.Errorf("worker of type %s: param %q carries a credential, so an explicit id is required", typ, key)
		}
		name := b.secretName(id, key)
		if err := storer.Put(name, s); err != nil {
			return nil, fmt.Errorf("worker %s: seal %q into the vault: %w", id, key, err)
		}
		out[key] = "vault:" + name
	}
	return out, nil
}

// secretName is the vault entry for one worker's credential: project, worker and
// param. Storing the same name again overwrites, so sealing is idempotent.
//
// Deleting a worker must NOT delete its entries: a credential may be shared, and
// a worker does not own a value it merely references.
func (b *workerHostBackend) secretName(id, param string) string {
	scope := b.projectID
	if scope == "" {
		scope = "project"
	}
	return scope + "/" + SanitizeID(id) + "/" + param
}

// Archive moves a worker in or out of the archived set. Archiving suspends a
// live worker first (so nothing runs behind a hidden manifest) and then drops
// its runtime identity; the durable identity is kept, so a restore resumes with
// the same grants. Unarchiving restores the manifest and resumes it.
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
		m, err := b.manifest(id)
		if err != nil {
			return err
		}
		if PlacementOf(*m) != coreworker.PlacementUnmanagedProcess {
			if err := b.suspend(id); err != nil {
				return fmt.Errorf("suspend before archive: %w", err)
			}
		}
		if err := b.manifests.Archive(id, true); err != nil {
			return err
		}
		b.dropRuntimeIdentity(id)
		b.logf("[worker %s] archived %s", b.projectID, id)
		return nil
	}

	if !b.isArchived(id) {
		// Already active (or unknown). Distinguish the two: restoring an active
		// worker is a no-op, restoring an unknown one is a mistake.
		if _, err := b.manifest(id); err != nil {
			return err
		}
		return nil
	}
	if err := b.manifests.Archive(id, false); err != nil {
		return err
	}
	// Resume covers every placement: an in-process worker is rebuilt, a
	// managed-process worker is relaunched, a dial-in worker gets its identity
	// and credential re-ensured.
	if err := b.resume(ctx, id); err != nil {
		return fmt.Errorf("restored manifest for %s but resume failed: %w", id, err)
	}
	b.logf("[worker %s] unarchived %s", b.projectID, id)
	return nil
}

// suspend / resume are the placement switchboards shared by the exported
// lifecycle methods and by Archive, which must not re-take the id lock.
func (b *workerHostBackend) suspend(id string) error {
	m, err := b.manifest(id)
	if err != nil {
		if b.manager != nil && b.manager.Has(id) {
			return b.manager.Suspend(id)
		}
		return err
	}
	switch PlacementOf(*m) {
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("worker %s: no process supervisor", id)
		}
		return b.supervisor.Stop(id)
	case coreworker.PlacementUnmanagedProcess:
		return nil // nothing local to stop
	default:
		if b.manager == nil || !b.manager.Has(id) {
			return nil // declared but not instantiated
		}
		return b.manager.Suspend(id)
	}
}

func (b *workerHostBackend) resume(ctx context.Context, id string) error {
	m, err := b.manifest(id)
	if err != nil {
		return err
	}
	switch PlacementOf(*m) {
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("worker %s: no process supervisor", id)
		}
		credential, err := process.ProvisionManagedProcess(b.registry, b.signer, b.bus, identityFromManifest(*m))
		if err != nil {
			return err
		}
		return b.supervisor.Start(b.launchSpec(*m, credential))
	case coreworker.PlacementUnmanagedProcess:
		_, err := process.ProvisionUnmanagedProcess(b.bus, b.signer, identityFromManifest(*m))
		return err
	default:
		if b.manager == nil {
			return fmt.Errorf("worker %s: no instance manager", id)
		}
		if b.manager.Has(id) {
			return b.manager.Resume(ctx, id)
		}
		_, err := b.manager.Create(ctx, SpawnConfig(*m))
		return err
	}
}

// dropRuntimeIdentity removes a worker's live bus channel and runtime identity,
// keeping the durable identity (grants and credential) intact.
func (b *workerHostBackend) dropRuntimeIdentity(id string) {
	if b.engine != nil && b.engine.Channel(id) != nil {
		b.engine.Disconnect(id) // also broadcasts worker.gone
	}
	if b.registry == nil {
		return
	}
	if _, ok := b.registry.Lookup(id); ok {
		if err := b.registry.Revoke(id); err != nil {
			b.logf("[worker %s] revoke runtime identity %s: %v", b.projectID, id, err)
		}
	}
}

func (b *workerHostBackend) manifest(id string) (*Manifest, error) {
	m, ok, err := b.manifests.Get(id)
	if err != nil {
		return nil, err
	}
	if ok {
		return m, nil
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
	m, err := b.manifest(id)
	if err != nil {
		// A live worker without a manifest (transient state) still obeys its
		// owner.
		if b.manager != nil && b.manager.Has(id) {
			return b.manager.Suspend(id)
		}
		return err
	}
	switch PlacementOf(*m) {
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("worker %s: no process supervisor", id)
		}
		return b.supervisor.Stop(id)
	case coreworker.PlacementUnmanagedProcess:
		return fmt.Errorf("worker %s runs outside this host and cannot be suspended", id)
	default:
		return b.manager.Suspend(id)
	}
}

func (b *workerHostBackend) Resume(ctx context.Context, id string) error {
	l := b.idLock(id)
	l.Lock()
	defer l.Unlock()
	return b.resume(ctx, id)
}

func (b *workerHostBackend) Restart(ctx context.Context, id string) error {
	l := b.idLock(id)
	l.Lock()
	defer l.Unlock()
	if b.isProtected(id) {
		return fmt.Errorf("worker %s is protected and cannot be restarted", id)
	}
	m, err := b.manifest(id)
	if err != nil {
		return err
	}
	switch PlacementOf(*m) {
	case coreworker.PlacementManagedProcess:
		if b.supervisor == nil {
			return fmt.Errorf("worker %s: no process supervisor", id)
		}
		if err := b.supervisor.Remove(id); err != nil {
			return err
		}
		credential, err := process.ProvisionManagedProcess(b.registry, b.signer, b.bus, identityFromManifest(*m))
		if err != nil {
			return err
		}
		return b.supervisor.Start(b.launchSpec(*m, credential))
	case coreworker.PlacementUnmanagedProcess:
		return fmt.Errorf("worker %s runs outside this host and cannot be restarted", id)
	default:
		return b.manager.Restart(ctx, SpawnConfig(*m))
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
	// owns that state (the manager owns state.json, the supervisor owns the
	// process log and the child's state dir); the manifest store does not.
	if b.manager != nil && b.manager.Has(id) {
		if err := b.manager.Destroy(id); err != nil {
			return err
		}
	} else if b.supervisor != nil {
		// Also covers an in-process worker that is not live in this run: its
		// state directory is under the same <workers>/<id> layout.
		if err := b.supervisor.Destroy(id); err != nil {
			return err
		}
	}

	// Drop the manifest, then the identity (durable and runtime).
	if err := b.manifests.Delete(id); err != nil {
		return err
	}
	if b.engine != nil && b.engine.Channel(id) != nil {
		b.engine.Disconnect(id) // also broadcasts worker.gone
	}
	if b.bus != nil {
		if err := b.bus.Revoke(id); err != nil {
			return err
		}
	}
	b.logf("[worker %s] destroyed %s", b.projectID, id)
	return nil
}

// ── Manifest read side ──

func (b *workerHostBackend) infoFor(m *Manifest) coreworker.WorkerInfo {
	vi := coreworker.WorkerInfo{
		ID:          m.ID,
		Type:        m.Type,
		Placement:   PlacementOf(*m),
		State:       "stopped",
		Label:       m.Label,
		Tags:        m.Tags,
		Description: m.Description,
		Params:      m.Params,
	}
	switch vi.Placement {
	case coreworker.PlacementManagedProcess:
		if b.supervisor != nil {
			if st, ok := b.supervisor.Get(m.ID); ok {
				vi.State = st.State
			}
		}
	case coreworker.PlacementUnmanagedProcess:
		// Nothing local to report: it is "stopped" from this host's point of
		// view (whether it is connected is the registry's business).
	default:
		if b.manager != nil {
			for _, wi := range b.manager.List("") {
				if wi.ID == m.ID {
					vi.State = wi.State
				}
			}
		}
	}
	return vi
}

// Get returns one active worker's view. An archived worker is not part of the
// active set and reports ok=false; restore it through Archive(id, false).
func (b *workerHostBackend) Get(id string) (coreworker.WorkerInfo, bool, error) {
	m, ok, err := b.manifests.Get(id)
	if err != nil {
		return coreworker.WorkerInfo{}, false, err
	}
	if ok {
		return b.infoFor(m), true, nil
	}
	// Not active. An archived worker keeps a suspended entry (archiving keeps
	// its snapshot so a restore can resume), and it must not resurface in the
	// active view.
	if b.isArchived(id) {
		return coreworker.WorkerInfo{}, false, nil
	}
	// A live worker that has no manifest yet (a spawn in flight).
	if b.manager != nil {
		for _, wi := range b.manager.List("") {
			if wi.ID == id {
				return coreworker.WorkerInfo{ID: wi.ID, Type: wi.Type, Placement: coreworker.PlacementInProcess, State: wi.State}, true, nil
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

// List returns the manifests of one set (active or archived) plus any live
// worker not (yet) declared. An archived worker is materialized in the archived
// view with Archived=true so a display layer can tell the two apart.
func (b *workerHostBackend) List(archived bool) []coreworker.WorkerInfo {
	manifests, err := b.manifests.List(archived)
	if err != nil {
		b.logf("[worker %s] list manifests: %v", b.projectID, err)
	}
	archivedIDs := map[string]bool{}
	if !archived {
		if other, err := b.manifests.List(true); err == nil {
			for _, m := range other {
				archivedIDs[m.ID] = true
			}
		}
	}
	seen := make(map[string]bool, len(manifests))
	out := make([]coreworker.WorkerInfo, 0, len(manifests))
	for _, m := range manifests {
		seen[m.ID] = true
		vi := b.infoFor(m)
		vi.Archived = archived
		out = append(out, vi)
	}
	if archived {
		return out
	}
	if b.manager != nil {
		for _, wi := range b.manager.List("") {
			if seen[wi.ID] || archivedIDs[wi.ID] {
				continue
			}
			out = append(out, coreworker.WorkerInfo{ID: wi.ID, Type: wi.Type, Placement: coreworker.PlacementInProcess, State: wi.State})
		}
	}
	if b.supervisor != nil {
		for _, st := range b.supervisor.List() {
			if seen[st.ID] || archivedIDs[st.ID] {
				continue
			}
			out = append(out, coreworker.WorkerInfo{ID: st.ID, Type: st.Type, Placement: coreworker.PlacementManagedProcess, State: st.State})
		}
	}
	return out
}

// isArchived reports whether id is in the archived file.
func (b *workerHostBackend) isArchived(id string) bool {
	manifests, err := b.manifests.List(true)
	if err != nil {
		b.logf("[worker %s] list archived: %v", b.projectID, err)
		return false
	}
	for _, m := range manifests {
		if m.ID == id {
			return true
		}
	}
	return false
}

// ── Manifest → process package inputs ──

// identityFromSpec lowers a spawn request into the provisioning input. The
// request's ACL is only a seed; provisioning never overwrites a stored grant.
func identityFromSpec(spec coreworker.WorkerSpec) process.IdentitySpec {
	return process.IdentitySpec{
		ID:            spec.ID,
		Type:          spec.Type,
		Subscriptions: spec.SubscribeAllow,
		Publish:       spec.PublishAllow,
	}
}

// identityFromManifest is the resume/restart input: no ACL (the durable record
// owns it), only identity.
func identityFromManifest(m Manifest) process.IdentitySpec {
	return process.IdentitySpec{ID: m.ID, Type: m.Type}
}

// launchSpec lowers a manifest into the supervisor's launch input. The
// credential is the freshly minted one (managed-process never persists it).
//
// Param values go through the assembly layer's secret provider: a `file:` /
// `env:` reference is expanded here, at the boundary where a declaration turns
// into a live process environment.
func (b *workerHostBackend) launchSpec(m Manifest, credential string) process.Spec {
	env := make(map[string]string, len(m.Params))
	for k, v := range m.Params {
		if s, ok := v.(string); ok {
			env[k] = b.secrets.Resolve(s)
		}
	}
	return process.Spec{
		ID:         m.ID,
		Type:       m.Type,
		Command:    m.Command,
		Env:        env,
		Cwd:        m.Cwd,
		Credential: credential,
	}
}

var _ coreworker.Backend = (*workerHostBackend)(nil)
