// Package workersvc provides WorkerService — the in-process worker lifecycle
// engine: register, create, suspend, resume, restart, destroy, recover.
//
// WorkerService owns worker construction via a per-type Builder registry
// (injected by the assembly layer) and the lifecycle state machine. It calls
// the Builder to obtain a SpawnSpec (connect/build closures), then manages
// the worker's connection, goroutine, and persisted runtime state.
//
// Concurrency: lifecycle operations are serialized per worker id. Slow
// operations (Connect, Build, Start, Snapshot, Stop, Close, store I/O) run
// outside the registry lock; the registry lock only protects the entry table.
// Checkpoint intentionally does not take the per-id lock: it is called from a
// worker's own durable-state callback and may run while that worker is being
// suspended. It only updates the cached snapshot under the registry lock.
package workersvc

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/worker"
)

// Builder constructs a SpawnSpec from a serializable WorkerConfig. Builders
// are provided by the assembly layer (project), which knows the concrete worker
// types and the bus transport.
type Builder func(cfg worker.WorkerConfig) (worker.SpawnSpec, error)

// workerEntry pairs a managed worker with its construction spec and lifecycle
// state.
type workerEntry struct {
	spec     worker.SpawnSpec
	ch       corebus.WorkerSideChannel // current bus connection (nil when suspended)
	worker   worker.ManagedWorker      // live instance (nil when suspended)
	typ      string
	state    worker.WorkerState
	snapshot []byte // last snapshot, taken at suspend time or a checkpoint
	snapSeq  uint64 // increments whenever snapshot is replaced
}

// WorkerService manages worker lifecycles. It has no awareness of event types
// or subscriptions — it only knows about worker.SpawnSpec, ManagedWorker and
// lifecycle operations. External effects are two explicit ports: WorkerStore
// for runtime state, and the onSpawn hook for declaring a runtime-created
// worker.
type WorkerService struct {
	entries  []workerEntry
	builders map[string]Builder
	// paramChecks is the per-type spawn-parameter validator, registered next to
	// the builder: "which parameters this type reads" is type knowledge the
	// mechanism does not have.
	paramChecks map[string]func(params map[string]any) error
	store       WorkerStore     // optional; nil disables persistence
	protected   map[string]bool // essential worker ids: cannot be suspended/destroyed
	onSpawn     func(cfg worker.WorkerConfig) error
	mu          sync.Mutex
	// locks serializes lifecycle operations per worker id, so create/suspend/
	// resume/restart/destroy cannot interleave for the same worker.
	locks map[string]*sync.Mutex
}

// New creates an empty WorkerService.
func New() *WorkerService {
	return &WorkerService{
		builders:    make(map[string]Builder),
		paramChecks: make(map[string]func(map[string]any) error),
		protected:   map[string]bool{},
		locks:       make(map[string]*sync.Mutex),
	}
}

func (s *WorkerService) isProtected(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.protected[id]
}

// idLock returns the per-worker lifecycle mutex. Call it BEFORE taking s.mu
// for the operation; the returned lock is held across the whole lifecycle
// transition.
func (s *WorkerService) idLock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[id]
	if !ok {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	return l
}

// SetStore attaches a persistence backend. May be nil to disable persistence.
func (s *WorkerService) SetStore(store WorkerStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

// SetSpawnHook sets the callback invoked after a worker is created: the
// declaring layer (project) uses it to persist the declaration of a worker
// spawned at runtime, so it survives a restart. Recovery does not go through
// it — declared workers are already on disk.
func (s *WorkerService) SetSpawnHook(fn func(cfg worker.WorkerConfig) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSpawn = fn
}

// RegisterBuilder registers a Builder for a worker type. CreateWorker dispatches
// to the registered builder for the config's Type.
func (s *WorkerService) RegisterBuilder(typ string, b Builder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.builders[typ] = b
}

// RegisterParamCheck registers the spawn-parameter validator for a worker type.
// It is registered next to its builder because the valid set is that type's own
// knowledge. CreateWorker runs it before building, so a caller learns that a
// parameter would be ignored instead of receiving a worker that quietly does not
// do what was asked. Recovery (RestoreAndRun / RestoreSuspended) deliberately
// skips it: a declaration already accepted must not be stranded by a stricter
// check added later.
func (s *WorkerService) RegisterParamCheck(typ string, check func(params map[string]any) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paramChecks[typ] = check
}

// paramCheck returns the registered validator for typ, or nil.
func (s *WorkerService) paramCheck(typ string) func(map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paramChecks[typ]
}

// RegisteredTypes returns the worker types this service can build, sorted. It
// is the set CreateWorker accepts; the assembly layer uses it to keep its
// per-type knowledge (such as spawn-parameter validation) in step with the
// builders it registered.
func (s *WorkerService) RegisteredTypes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	types := make([]string, 0, len(s.builders))
	for typ := range s.builders {
		types = append(types, typ)
	}
	sort.Strings(types)
	return types
}

// ValidateSpawn reports whether a config could be built: a builder is
// registered for its type and that type's spawn-parameter check passes. It
// lets a caller validate before persisting a declaration, so a rejected spawn
// leaves nothing behind.
func (s *WorkerService) ValidateSpawn(cfg worker.WorkerConfig) error {
	s.mu.Lock()
	_, ok := s.builders[cfg.Type]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("workersvc: no builder for type %q", cfg.Type)
	}
	if check := s.paramCheck(cfg.Type); check != nil {
		if err := check(cfg.Params); err != nil {
			return fmt.Errorf("workersvc: build %q: %w", cfg.ID, err)
		}
	}
	return nil
}

// CreateWorker builds and starts a worker from a serializable config. It
// dispatches to the registered builder for cfg.Type, then spawns it. The type's
// spawn-parameter check runs first, so an unsupported parameter fails here
// rather than being silently dropped by the builder.
//
// It returns the config the builder actually built (the spec's Config, not the
// input): a builder may derive the worker's id or complete its params, and that
// resolved config — not the request — is the worker's identity on the bus and in
// its declaration.
func (s *WorkerService) CreateWorker(ctx context.Context, cfg worker.WorkerConfig) (worker.WorkerConfig, error) {
	if err := s.ValidateSpawn(cfg); err != nil {
		return worker.WorkerConfig{}, err
	}
	s.mu.Lock()
	b := s.builders[cfg.Type]
	s.mu.Unlock()
	spec, err := b(cfg)
	if err != nil {
		return worker.WorkerConfig{}, fmt.Errorf("workersvc: build %q: %w", cfg.ID, err)
	}

	l := s.idLock(spec.ID())
	l.Lock()
	defer l.Unlock()
	if s.HasWorker(spec.ID()) {
		return worker.WorkerConfig{}, fmt.Errorf("workersvc: worker %s already exists", spec.ID())
	}
	if err := s.spawn(ctx, spec); err != nil {
		return worker.WorkerConfig{}, err
	}
	// The worker is running; let the declaring layer record it (a project
	// appends it to workers.json). The declared config is the RESOLVED one, so a
	// derived id is never lost. If declaring fails, roll the worker back rather
	// than leave a live worker with no durable declaration.
	if s.onSpawn != nil {
		if err := s.onSpawn(spec.Config); err != nil {
			_ = s.destroyLocked(spec.ID())
			return spec.Config, fmt.Errorf("workersvc: declare %q: %w", spec.ID(), err)
		}
	}
	return spec.Config, nil
}

// spawn connects, builds and starts a worker from a SpawnSpec, then records it.
// The caller holds the worker's id lock.
func (s *WorkerService) spawn(ctx context.Context, spec worker.SpawnSpec) error {
	// Check before opening a connection: a duplicate must not leak a worker or
	// a channel. The id lock makes this a definitive check.
	if s.HasWorker(spec.ID()) {
		return fmt.Errorf("workersvc: worker %s already exists", spec.ID())
	}
	ch, err := spec.Connect()
	if err != nil {
		return fmt.Errorf("workersvc: connect %q: %w", spec.ID(), err)
	}
	w := spec.Build(ch)
	if err := w.Start(ctx); err != nil {
		_ = ch.Close()
		return fmt.Errorf("workersvc: start %q: %w", spec.ID(), err)
	}

	s.mu.Lock()
	if s.findLocked(spec.ID()) != nil {
		s.mu.Unlock()
		_ = w.Stop()
		_ = ch.Close()
		return fmt.Errorf("workersvc: worker %s already exists", spec.ID())
	}
	s.entries = append(s.entries, workerEntry{
		spec:   spec,
		ch:     ch,
		worker: w,
		typ:    spec.Type(),
		state:  worker.StateRunning,
	})
	store := s.store
	s.mu.Unlock()
	if store != nil {
		if err := store.SaveState(spec.ID(), worker.StateRunning, nil); err != nil {
			log.Printf("[workersvc] persist state %s: %v", spec.ID(), err)
		}
	}
	log.Printf("[workersvc] created worker %s (type=%s)", spec.ID(), spec.Type())
	return nil
}

// RestoreAndRun materializes a worker from its persisted params and snapshot
// and starts it running — full recovery of a previously-persisted worker,
// making persisted state authoritative over any (template-sourced) definition.
// When snapshot is non-empty it is applied before Start. Used by project
// recovery; it records the worker as running and persists it again.
func (s *WorkerService) RestoreAndRun(ctx context.Context, cfg worker.WorkerConfig, snapshot []byte) error {
	s.mu.Lock()
	b, ok := s.builders[cfg.Type]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("workersvc: no builder for type %q", cfg.Type)
	}
	spec, err := b(cfg)
	if err != nil {
		return fmt.Errorf("workersvc: build %q: %w", cfg.ID, err)
	}
	l := s.idLock(spec.ID())
	l.Lock()
	defer l.Unlock()
	return s.restoreAndRunLocked(ctx, spec, snapshot)
}

// restoreAndRunLocked connects/builds/restores/starts a worker and records it.
// The caller holds the worker's id lock; the duplicate check happens before a
// connection is opened, and a race is cleaned up rather than leaked.
func (s *WorkerService) restoreAndRunLocked(ctx context.Context, spec worker.SpawnSpec, snapshot []byte) error {
	if s.HasWorker(spec.ID()) {
		return fmt.Errorf("workersvc: worker %s already exists", spec.ID())
	}
	ch, err := spec.Connect()
	if err != nil {
		return fmt.Errorf("workersvc: connect %q: %w", spec.ID(), err)
	}
	w := spec.Build(ch)
	if len(snapshot) > 0 {
		if err := w.Restore(snapshot); err != nil {
			_ = ch.Close()
			return fmt.Errorf("workersvc: restore %q: %w", spec.ID(), err)
		}
	}
	if err := w.Start(ctx); err != nil {
		_ = ch.Close()
		return fmt.Errorf("workersvc: start %q: %w", spec.ID(), err)
	}

	s.mu.Lock()
	if s.findLocked(spec.ID()) != nil {
		s.mu.Unlock()
		_ = w.Stop()
		_ = ch.Close()
		return fmt.Errorf("workersvc: worker %s already exists", spec.ID())
	}
	s.entries = append(s.entries, workerEntry{
		spec:   spec,
		ch:     ch,
		worker: w,
		typ:    spec.Type(),
		state:  worker.StateRunning,
	})
	store := s.store
	s.mu.Unlock()
	if store != nil {
		if err := store.SaveState(spec.ID(), worker.StateRunning, snapshot); err != nil {
			log.Printf("[workersvc] persist state %s: %v", spec.ID(), err)
		}
	}
	log.Printf("[workersvc] restored worker %s (type=%s)", spec.ID(), spec.Type())
	return nil
}

// SuspendWorker stops a running worker: snapshots its state, stops its
// goroutine, and releases its bus connection. The worker's identity and
// definition are retained so it can be resumed. no-op if already suspended.
func (s *WorkerService) SuspendWorker(id string) error {
	l := s.idLock(id)
	l.Lock()
	defer l.Unlock()
	return s.suspendLocked(id)
}

// suspendLocked snapshots and stops a worker. The caller holds its id lock.
// Snapshot/Stop run outside s.mu: they can be slow and may call back into the
// service (durable checkpoints), so holding the registry lock would stall every
// worker operation.
func (s *WorkerService) suspendLocked(id string) error {
	s.mu.Lock()
	if s.protected[id] {
		s.mu.Unlock()
		return fmt.Errorf("workersvc: worker %s is protected and cannot be suspended", id)
	}
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workersvc: worker %s not found", id)
	}
	if e.state == worker.StateSuspended {
		s.mu.Unlock()
		return nil
	}
	w := e.worker
	ch := e.ch
	seq := e.snapSeq
	s.mu.Unlock()

	// Snapshot must happen before stopping so we capture a consistent state.
	snap, err := w.Snapshot()
	if err != nil {
		return fmt.Errorf("workersvc: snapshot %s: %w", id, err)
	}
	if err := w.Stop(); err != nil {
		return fmt.Errorf("workersvc: stop %s: %w", id, err)
	}
	if ch != nil {
		_ = ch.Close()
	}

	s.mu.Lock()
	if e := s.findLocked(id); e != nil {
		// A checkpoint may have completed while we were stopping. If so, its
		// snapshot is newer than the one we took before Stop; keep it. If not,
		// install ours. Either way the persisted snapshot is the one the entry
		// now holds.
		if e.snapSeq != seq {
			snap = e.snapshot
		} else {
			e.snapshot = snap
			e.snapSeq++
		}
		e.ch = nil
		e.worker = nil
		e.state = worker.StateSuspended
	}
	store := s.store
	s.mu.Unlock()
	log.Printf("[workersvc] suspended worker %s", id)
	if store != nil {
		if err := store.SaveState(id, worker.StateSuspended, snap); err != nil {
			log.Printf("[workersvc] persist state %s: %v", id, err)
		}
	}
	return nil
}

// ResumeWorker restores a suspended worker: reconnects its bus channel,
// rebuilds the instance, restores its snapshot, and starts it. no-op if running.
func (s *WorkerService) ResumeWorker(ctx context.Context, id string) error {
	l := s.idLock(id)
	l.Lock()
	defer l.Unlock()

	s.mu.Lock()
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workersvc: worker %s not found", id)
	}
	if e.state == worker.StateRunning {
		s.mu.Unlock()
		return nil
	}
	spec := e.spec
	snapshot := e.snapshot
	s.mu.Unlock()

	ch, err := spec.Connect()
	if err != nil {
		return fmt.Errorf("workersvc: reconnect %q: %w", id, err)
	}
	w := spec.Build(ch)
	if snapshot != nil {
		if err := w.Restore(snapshot); err != nil {
			_ = ch.Close()
			return fmt.Errorf("workersvc: restore %q: %w", id, err)
		}
	}
	if err := w.Start(ctx); err != nil {
		_ = ch.Close()
		return fmt.Errorf("workersvc: start %q: %w", id, err)
	}

	s.mu.Lock()
	if e := s.findLocked(id); e != nil {
		e.ch = ch
		e.worker = w
		e.state = worker.StateRunning
	}
	store := s.store
	s.mu.Unlock()
	log.Printf("[workersvc] resumed worker %s", id)
	if store != nil {
		if err := store.SaveState(id, worker.StateRunning, snapshot); err != nil {
			log.Printf("[workersvc] persist state %s: %v", id, err)
		}
	}
	return nil
}

// Checkpoint snapshots a running worker and persists it, so a state change
// made at runtime (e.g. a reason worker switching its LLM provider) reaches
// disk without waiting for a suspend or a clean shutdown. It is the worker's
// own "durable state changed" signal, delivered by whatever layer wired the
// callback. Errors from the store are returned; the caller decides what they
// mean (the assembly layer logs them).
//
// Safe to call from the worker's own goroutine: it holds the service lock only
// to look the entry up, and takes the snapshot outside it — the snapshot
// itself takes the worker's lock, so the caller must not hold it.
func (s *WorkerService) Checkpoint(id string) error {
	s.mu.Lock()
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workersvc: worker %s not found", id)
	}
	if e.worker == nil {
		s.mu.Unlock()
		return fmt.Errorf("workersvc: worker %s is not running (nothing to snapshot)", id)
	}
	w := e.worker
	store := s.store
	s.mu.Unlock()

	if store == nil {
		return nil
	}
	snap, err := w.Snapshot()
	if err != nil {
		return fmt.Errorf("workersvc: snapshot %s: %w", id, err)
	}

	// Keep the entry's cached snapshot in step: it is what a later
	// ResumeWorker replays. Do not let a checkpoint that raced with suspend or
	// destroy resurrect a worker that is no longer running.
	s.mu.Lock()
	e = s.findLocked(id)
	if e == nil || e.worker != w || e.state != worker.StateRunning {
		s.mu.Unlock()
		return fmt.Errorf("workersvc: worker %s stopped while checkpointing", id)
	}
	e.snapshot = snap
	e.snapSeq++
	s.mu.Unlock()

	if err := store.SaveState(id, worker.StateRunning, snap); err != nil {
		return fmt.Errorf("workersvc: persist state %s: %w", id, err)
	}
	return nil
}

// DestroyWorker stops a worker, removes it from the registry and deletes its
// persisted state.
func (s *WorkerService) DestroyWorker(id string) error {
	l := s.idLock(id)
	l.Lock()
	defer l.Unlock()
	return s.destroyLocked(id)
}

// destroyLocked stops the worker first, then removes the entry and its
// persisted state. The caller holds the id lock. Stop/Close run outside s.mu.
// If Stop fails the entry is left in place and no state is deleted: a failed
// destroy must not make a still-live worker invisible.
func (s *WorkerService) destroyLocked(id string) error {
	s.mu.Lock()
	if s.protected[id] {
		s.mu.Unlock()
		return fmt.Errorf("workersvc: worker %s is protected and cannot be destroyed", id)
	}
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workersvc: worker %s not found", id)
	}
	w := e.worker
	ch := e.ch
	store := s.store
	s.mu.Unlock()

	if w != nil {
		if err := w.Stop(); err != nil {
			return fmt.Errorf("workersvc: stop %s: %w", id, err)
		}
	}
	if ch != nil {
		_ = ch.Close()
	}

	s.mu.Lock()
	s.entries = slices.DeleteFunc(s.entries, func(en workerEntry) bool { return en.spec.ID() == id })
	s.mu.Unlock()
	if store != nil {
		_ = store.Delete(id)
	}
	log.Printf("[workersvc] destroyed worker %s", id)
	return nil
}

// RestartWorker brings a worker up again from cfg while keeping the state it
// already persisted.
//
// A restart is the same sequence every start follows — build the instance from
// the (current) definition, then replay the persisted snapshot over it — so
// "first start", "process recovery", "resume" and "restart" all mean one
// thing: config supplies the construction inputs, the snapshot supplies what
// the worker learned. Construction-only params (budgets, context window, tail
// retention, subscriptions, publish grants, approver) therefore follow a
// declaration edit on the next start; fields the snapshot owns — a reason
// worker's provider selection, its programs and readme, a workspace's mounts,
// an mcp worker's servers, a timer's armed set — survive it, and are changed
// through the runtime events that own them (provider.switch,
// worker.readme.update, mount.add, mcp.server.add, …), not by editing config.
//
// A running instance is snapshotted before it is replaced, so a restart never
// loses what the worker learned. A type change is the one case that cannot
// carry a snapshot: the blob belongs to a different worker implementation, so
// it is dropped rather than replayed into an instance that cannot read it.
//
// The id does not have to be live — an unknown id is simply started, which lets
// callers use one path for "restart" and "start from declaration".
func (s *WorkerService) RestartWorker(ctx context.Context, cfg worker.WorkerConfig) error {
	s.mu.Lock()
	b, ok := s.builders[cfg.Type]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("workersvc: no builder for type %q", cfg.Type)
	}
	spec, err := b(cfg)
	if err != nil {
		return fmt.Errorf("workersvc: build %q: %w", cfg.ID, err)
	}

	l := s.idLock(spec.ID())
	l.Lock()
	defer l.Unlock()

	if !s.HasWorker(spec.ID()) {
		return s.restoreAndRunLocked(ctx, spec, s.persistedSnapshot(spec.ID()))
	}
	if s.isProtected(spec.ID()) {
		return fmt.Errorf("workersvc: worker %s is protected and cannot be restarted", spec.ID())
	}

	// Suspend snapshots before stopping and persists the result, so the state a
	// restart has to keep is on disk before the live instance goes away.
	if err := s.suspendLocked(spec.ID()); err != nil {
		return fmt.Errorf("workersvc: restart %q: %w", spec.ID(), err)
	}

	s.mu.Lock()
	e := s.findLocked(spec.ID())
	var prevType string
	var cached []byte
	if e != nil {
		prevType = e.typ
		cached = e.snapshot
		s.entries = slices.DeleteFunc(s.entries, func(en workerEntry) bool { return en.spec.ID() == spec.ID() })
	}
	s.mu.Unlock()

	// Prefer the instance's own snapshot: it was taken just now, while the disk
	// copy is only as fresh as the last successful persist.
	snap := cached
	if len(snap) == 0 {
		snap = s.persistedSnapshot(spec.ID())
	}
	if prevType != "" && prevType != spec.Type() {
		log.Printf("[workersvc] restart %s: type %s -> %s, dropping the %s snapshot", spec.ID(), prevType, spec.Type(), prevType)
		snap = nil
	}
	return s.restoreAndRunLocked(ctx, spec, snap)
}

// persistedSnapshot returns the snapshot persisted for id, or nil when there is
// none: no store, no record for that worker, or an unreadable store. It is the
// disk copy of the state a start has to replay.
func (s *WorkerService) persistedSnapshot(id string) []byte {
	if s.store == nil {
		return nil
	}
	recs, err := s.store.LoadAll()
	if err != nil {
		log.Printf("[workersvc] read persisted state %s: %v", id, err)
		return nil
	}
	for _, rec := range recs {
		if rec.ID == id {
			return rec.Snapshot
		}
	}
	return nil
}

// ShutdownSnapshot snapshots every running worker and persists it, so a clean
// shutdown leaves the latest state on disk. Called on process shutdown.
func (s *WorkerService) ShutdownSnapshot() {
	type target struct {
		id  string
		w   worker.ManagedWorker
		seq uint64
	}
	s.mu.Lock()
	store := s.store
	targets := make([]target, 0, len(s.entries))
	for i := range s.entries {
		e := &s.entries[i]
		if e.worker != nil {
			targets = append(targets, target{id: e.spec.ID(), w: e.worker, seq: e.snapSeq})
		}
	}
	s.mu.Unlock()

	for _, t := range targets {
		snap, err := t.w.Snapshot()
		if err != nil {
			log.Printf("[workersvc] shutdown snapshot %s: %v", t.id, err)
			continue
		}
		s.mu.Lock()
		if e := s.findLocked(t.id); e != nil {
			if e.snapSeq != t.seq {
				snap = e.snapshot
			} else {
				e.snapshot = snap
				e.snapSeq++
			}
		}
		s.mu.Unlock()
		if store != nil {
			if err := store.SaveState(t.id, worker.StateRunning, snap); err != nil {
				log.Printf("[workersvc] persist shutdown state %s: %v", t.id, err)
			}
		}
	}
}

// ListWorkers returns info for all managed workers, optionally filtered by type.
func (s *WorkerService) ListWorkers(typ string) []WorkerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []WorkerInfo
	for _, e := range s.entries {
		if typ != "" && e.typ != typ {
			continue
		}
		out = append(out, WorkerInfo{
			ID:    e.spec.ID(),
			Type:  e.typ,
			State: e.state,
		})
	}
	return out
}

// WorkerInfo is a read-only view of a managed worker.
type WorkerInfo struct {
	ID    string             `json:"id"`
	Type  string             `json:"type"`
	State worker.WorkerState `json:"state"`
}

// WorkerType returns the type label for the given worker ID, or false.
func (s *WorkerService) WorkerType(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findLocked(id); e != nil {
		return e.typ, true
	}
	return "", false
}

// HasWorker reports whether the given worker ID is managed by this service.
func (s *WorkerService) HasWorker(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findLocked(id) != nil
}

// Worker returns the live ManagedWorker instance for a running worker, or nil.
func (s *WorkerService) Worker(id string) (worker.ManagedWorker, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findLocked(id); e != nil && e.worker != nil {
		return e.worker, true
	}
	return nil, false
}

// LoadAllWorkers returns every persisted worker state record from the store.
func (s *WorkerService) LoadAllWorkers() ([]StateRecord, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.LoadAll()
}

// RestoreSuspended re-materializes a persisted worker as suspended — it builds
// the SpawnSpec via the registered builder but does not connect or start it.
// The snapshot from the persisted record is retained on the entry so a later
// ResumeWorker replays it (without it, a resumed worker would start empty).
// The worker is available for a later manual ResumeWorker.
func (s *WorkerService) RestoreSuspended(cfg worker.WorkerConfig, snapshot []byte) error {
	s.mu.Lock()
	b, ok := s.builders[cfg.Type]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("workersvc: no builder for type %q", cfg.Type)
	}
	spec, err := b(cfg)
	if err != nil {
		return fmt.Errorf("workersvc: build %q: %w", cfg.ID, err)
	}

	l := s.idLock(spec.ID())
	l.Lock()
	defer l.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findLocked(spec.ID()) != nil {
		return nil
	}
	s.entries = append(s.entries, workerEntry{
		spec:     spec,
		typ:      spec.Type(),
		state:    worker.StateSuspended,
		snapshot: snapshot,
	})
	log.Printf("[workersvc] restored suspended worker %s (type=%s)", spec.ID(), spec.Type())
	return nil
}

// RecoverOptions parameterizes RecoverAll: which workers must exist and run,
// and which must be suspended regardless of their persisted state.
type RecoverOptions struct {
	// Workers is the declared worker set to recover, with its config. The
	// store only holds runtime state, so the definition comes from here; a
	// persisted state without a declaration is an orphan and is skipped.
	Workers []worker.WorkerConfig
	// Essential workers must exist and run: created fresh when absent from the
	// store, restored to running even when persisted suspended. Their ids are
	// protected — SuspendWorker and DestroyWorker refuse them.
	Essential []worker.WorkerConfig
	// Suspend lists ids that must be suspended regardless of persisted state
	// (e.g. the webui worker when the webui is not started). Ignored for
	// Essential workers.
	Suspend []string
	// Protect lists ids that are protected from suspend / restart / destroy
	// without being forced to run. Essential ids are always protected; this is
	// for control-plane workers the assembly wants to keep alive (the host)
	// without materializing them when a template does not declare them.
	Protect []string
}

// RecoverAll restores the whole worker set: every declared worker is
// re-materialized per its persisted state (running → restore+run, suspended →
// suspended), then the Essential/Suspend overrides are applied. It is the
// single startup entry point — the assembly layer only boots the service.
func (s *WorkerService) RecoverAll(ctx context.Context, opts RecoverOptions) error {
	recs, err := s.LoadAllWorkers()
	if err != nil {
		return fmt.Errorf("workersvc: load all: %w", err)
	}
	byID := map[string]StateRecord{}
	for _, rec := range recs {
		byID[rec.ID] = rec
	}

	// Essential ids are protected from suspend/destroy.
	essentialIDs := map[string]bool{}
	for _, cfg := range opts.Essential {
		essentialIDs[cfg.ID] = true
	}
	for _, id := range opts.Protect {
		essentialIDs[id] = true
	}
	s.mu.Lock()
	s.protected = essentialIDs
	s.mu.Unlock()

	suspended := map[string]bool{}
	for _, id := range opts.Suspend {
		suspended[id] = true
	}

	restored := map[string]bool{}
	restoreOne := func(id string, cfg worker.WorkerConfig, rec StateRecord, forceRun bool) error {
		restored[id] = true
		if !forceRun && suspended[id] {
			return s.RestoreSuspended(cfg, rec.Snapshot)
		}
		if forceRun || rec.State == worker.StateRunning {
			if len(rec.Snapshot) > 0 {
				return s.RestoreAndRun(ctx, cfg, rec.Snapshot)
			}
			_, err := s.CreateWorker(ctx, cfg)
			return err
		}
		return s.RestoreSuspended(cfg, rec.Snapshot)
	}

	for _, cfg := range opts.Essential {
		rec, ok := byID[cfg.ID]
		if !ok {
			if err := s.RestoreAndRun(ctx, cfg, nil); err != nil {
				return fmt.Errorf("workersvc: recover essential %q: %w", cfg.ID, err)
			}
			restored[cfg.ID] = true
			continue
		}
		if err := restoreOne(cfg.ID, cfg, rec, true); err != nil {
			return fmt.Errorf("workersvc: recover essential %q: %w", cfg.ID, err)
		}
	}

	for _, cfg := range opts.Workers {
		if restored[cfg.ID] {
			continue
		}
		rec, ok := byID[cfg.ID]
		if !ok {
			// Declared but never started: no persisted state yet means a
			// fresh start, not a suspended one.
			rec = StateRecord{ID: cfg.ID, State: worker.StateRunning}
		}
		if err := restoreOne(cfg.ID, cfg, rec, false); err != nil {
			log.Printf("[workersvc] recover worker %s: %v", cfg.ID, err)
		}
	}
	return nil
}

// StopAll stops all registered workers in reverse registration order.
func (s *WorkerService) StopAll() {
	s.mu.Lock()
	workers := make([]worker.ManagedWorker, 0, len(s.entries))
	for _, e := range slices.Backward(s.entries) {
		if e.worker != nil {
			workers = append(workers, e.worker)
		}
	}
	s.mu.Unlock()
	for _, w := range workers {
		log.Printf("[workersvc] stopping worker %s", w.ID())
		_ = w.Stop()
	}
}

// Run blocks until ctx is cancelled, then snapshots and stops every worker.
func (s *WorkerService) Run(ctx context.Context) error {
	<-ctx.Done()
	log.Println("[workersvc] shutting down")
	s.ShutdownSnapshot()
	s.StopAll()
	return ctx.Err()
}
func (s *WorkerService) findLocked(id string) *workerEntry {
	for i := range s.entries {
		if s.entries[i].spec.ID() == id {
			return &s.entries[i]
		}
	}
	return nil
}
