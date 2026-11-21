// Package workerctl manages live worker instances: construct, connect, start,
// snapshot, suspend, resume, restart, destroy, and their persisted state.
//
// It knows nothing about bus semantics or placements. It receives a Build
// function (the type registry lives in the assembly layer) and turns a
// serializable WorkerConfig into a SpawnSpec, then owns the instance, its
// connection, its goroutine and its snapshot. External effects are two explicit
// ports: WorkerStore for runtime state, and the spawn hook for recording a
// runtime-created worker.
//
// Concurrency: lifecycle operations are serialized per worker id. Slow
// operations (Connect, Build, Start, Snapshot, Stop, Close, store I/O) run
// outside the registry lock; the registry lock only protects the entry table.
// Checkpoint intentionally does not take the per-id lock: it is called from a
// worker's own durable-state callback and may run while that worker is being
// suspended. It only updates the cached snapshot under the registry lock.
package workerctl

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/worker"
)

// Builder constructs a SpawnSpec from a serializable WorkerConfig. It is
// supplied by the assembly layer, which knows the concrete worker types and the
// bus transport.
type Builder func(cfg worker.WorkerConfig) (worker.SpawnSpec, error)

// Config parameterizes a Manager.
type Config struct {
	// Store persists runtime state and snapshots. Optional: nil disables
	// persistence (tests, ephemeral hosts).
	Store WorkerStore
	// Build resolves a config into a SpawnSpec. Required.
	Build Builder
}

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

// Manager manages worker lifecycles. It has no awareness of event types,
// subscriptions or placements — it only knows about worker.SpawnSpec,
// ManagedWorker and lifecycle operations.
type Manager struct {
	entries   []workerEntry
	build     Builder
	store     WorkerStore     // optional; nil disables persistence
	protected map[string]bool // protected worker ids: cannot be suspended/destroyed
	onSpawn   func(cfg worker.WorkerConfig) error
	mu        sync.Mutex
	// locks serializes lifecycle operations per worker id, so create/suspend/
	// resume/restart/destroy cannot interleave for the same worker.
	locks map[string]*sync.Mutex
}

// New creates a Manager over cfg.
func New(cfg Config) *Manager {
	return &Manager{
		build:     cfg.Build,
		store:     cfg.Store,
		protected: map[string]bool{},
		locks:     make(map[string]*sync.Mutex),
	}
}

// buildSpec runs the injected builder, naming the wrapper clearly in errors.
func (m *Manager) buildSpec(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	if m.build == nil {
		return worker.SpawnSpec{}, fmt.Errorf("workerctl: no builder configured")
	}
	spec, err := m.build(cfg)
	if err != nil {
		return worker.SpawnSpec{}, fmt.Errorf("workerctl: build %q: %w", cfg.ID, err)
	}
	return spec, nil
}

func (s *Manager) isProtected(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.protected[id]
}

// idLock returns the per-worker lifecycle mutex. Call it BEFORE taking s.mu
// for the operation; the returned lock is held across the whole lifecycle
// transition.
func (s *Manager) idLock(id string) *sync.Mutex {
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
func (s *Manager) SetStore(store WorkerStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

// SetSpawnHook sets the callback invoked after a worker is created: the
// declaring layer (project) uses it to persist the declaration of a worker
// spawned at runtime, so it survives a restart. Recovery does not go through
// it — declared workers are already on disk.
func (s *Manager) SetSpawnHook(fn func(cfg worker.WorkerConfig) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSpawn = fn
}

// Create builds and starts a worker from a serializable config. It calls the
// injected Builder, then spawns the result. Parameter validation belongs to the
// type registry in the assembly layer, which calls it before Create when it
// wants a rejected spawn to leave nothing behind.
//
// It returns the config the builder actually built (the spec's Config, not the
// input): a builder may derive the worker's id or complete its params, and that
// resolved config — not the request — is the worker's identity on the bus and in
// its declaration.
func (s *Manager) Create(ctx context.Context, cfg worker.WorkerConfig) (worker.WorkerConfig, error) {
	spec, err := s.buildSpec(cfg)
	if err != nil {
		return worker.WorkerConfig{}, err
	}

	l := s.idLock(spec.ID())
	l.Lock()
	defer l.Unlock()
	if s.Has(spec.ID()) {
		return worker.WorkerConfig{}, fmt.Errorf("workerctl: worker %s already exists", spec.ID())
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
			return spec.Config, fmt.Errorf("workerctl: declare %q: %w", spec.ID(), err)
		}
	}
	return spec.Config, nil
}

// spawn connects, builds and starts a worker from a SpawnSpec, then records it.
// The caller holds the worker's id lock.
func (s *Manager) spawn(ctx context.Context, spec worker.SpawnSpec) error {
	// Check before opening a connection: a duplicate must not leak a worker or
	// a channel. The id lock makes this a definitive check.
	if s.Has(spec.ID()) {
		return fmt.Errorf("workerctl: worker %s already exists", spec.ID())
	}
	ch, err := spec.Connect()
	if err != nil {
		return fmt.Errorf("workerctl: connect %q: %w", spec.ID(), err)
	}
	w := spec.Build(ch)
	if err := w.Start(ctx); err != nil {
		_ = ch.Close()
		return fmt.Errorf("workerctl: start %q: %w", spec.ID(), err)
	}

	s.mu.Lock()
	if s.findLocked(spec.ID()) != nil {
		s.mu.Unlock()
		_ = w.Stop()
		_ = ch.Close()
		return fmt.Errorf("workerctl: worker %s already exists", spec.ID())
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
			log.Printf("[workerctl] persist state %s: %v", spec.ID(), err)
		}
	}
	log.Printf("[workerctl] created worker %s (type=%s)", spec.ID(), spec.Type())
	return nil
}

// RestoreAndRun materializes a worker from its persisted params and snapshot
// and starts it running — full recovery of a previously-persisted worker,
// making persisted state authoritative over any (template-sourced) definition.
// When snapshot is non-empty it is applied before Start. Used by project
// recovery; it records the worker as running and persists it again.
func (s *Manager) RestoreAndRun(ctx context.Context, cfg worker.WorkerConfig, snapshot []byte) error {
	spec, err := s.buildSpec(cfg)
	if err != nil {
		return err
	}
	l := s.idLock(spec.ID())
	l.Lock()
	defer l.Unlock()
	return s.restoreAndRunLocked(ctx, spec, snapshot)
}

// restoreAndRunLocked connects/builds/restores/starts a worker and records it.
// The caller holds the worker's id lock; the duplicate check happens before a
// connection is opened, and a race is cleaned up rather than leaked.
func (s *Manager) restoreAndRunLocked(ctx context.Context, spec worker.SpawnSpec, snapshot []byte) error {
	if s.Has(spec.ID()) {
		return fmt.Errorf("workerctl: worker %s already exists", spec.ID())
	}
	ch, err := spec.Connect()
	if err != nil {
		return fmt.Errorf("workerctl: connect %q: %w", spec.ID(), err)
	}
	w := spec.Build(ch)
	if len(snapshot) > 0 {
		if err := w.Restore(snapshot); err != nil {
			_ = ch.Close()
			return fmt.Errorf("workerctl: restore %q: %w", spec.ID(), err)
		}
	}
	if err := w.Start(ctx); err != nil {
		_ = ch.Close()
		return fmt.Errorf("workerctl: start %q: %w", spec.ID(), err)
	}

	s.mu.Lock()
	if s.findLocked(spec.ID()) != nil {
		s.mu.Unlock()
		_ = w.Stop()
		_ = ch.Close()
		return fmt.Errorf("workerctl: worker %s already exists", spec.ID())
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
			log.Printf("[workerctl] persist state %s: %v", spec.ID(), err)
		}
	}
	log.Printf("[workerctl] restored worker %s (type=%s)", spec.ID(), spec.Type())
	return nil
}

// Suspend stops a running worker: snapshots its state, stops its
// goroutine, and releases its bus connection. The worker's identity and
// definition are retained so it can be resumed. no-op if already suspended.
func (s *Manager) Suspend(id string) error {
	l := s.idLock(id)
	l.Lock()
	defer l.Unlock()
	return s.suspendLocked(id)
}

// suspendLocked snapshots and stops a worker. The caller holds its id lock.
// Snapshot/Stop run outside s.mu: they can be slow and may call back into the
// service (durable checkpoints), so holding the registry lock would stall every
// worker operation.
func (s *Manager) suspendLocked(id string) error {
	s.mu.Lock()
	if s.protected[id] {
		s.mu.Unlock()
		return fmt.Errorf("workerctl: worker %s is protected and cannot be suspended", id)
	}
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workerctl: worker %s not found", id)
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
		return fmt.Errorf("workerctl: snapshot %s: %w", id, err)
	}
	if err := w.Stop(); err != nil {
		return fmt.Errorf("workerctl: stop %s: %w", id, err)
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
	log.Printf("[workerctl] suspended worker %s", id)
	if store != nil {
		if err := store.SaveState(id, worker.StateSuspended, snap); err != nil {
			log.Printf("[workerctl] persist state %s: %v", id, err)
		}
	}
	return nil
}

// Resume restores a suspended worker: reconnects its bus channel,
// rebuilds the instance, restores its snapshot, and starts it. no-op if running.
func (s *Manager) Resume(ctx context.Context, id string) error {
	l := s.idLock(id)
	l.Lock()
	defer l.Unlock()

	s.mu.Lock()
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workerctl: worker %s not found", id)
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
		return fmt.Errorf("workerctl: reconnect %q: %w", id, err)
	}
	w := spec.Build(ch)
	if snapshot != nil {
		if err := w.Restore(snapshot); err != nil {
			_ = ch.Close()
			return fmt.Errorf("workerctl: restore %q: %w", id, err)
		}
	}
	if err := w.Start(ctx); err != nil {
		_ = ch.Close()
		return fmt.Errorf("workerctl: start %q: %w", id, err)
	}

	s.mu.Lock()
	if e := s.findLocked(id); e != nil {
		e.ch = ch
		e.worker = w
		e.state = worker.StateRunning
	}
	store := s.store
	s.mu.Unlock()
	log.Printf("[workerctl] resumed worker %s", id)
	if store != nil {
		if err := store.SaveState(id, worker.StateRunning, snapshot); err != nil {
			log.Printf("[workerctl] persist state %s: %v", id, err)
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
func (s *Manager) Checkpoint(id string) error {
	s.mu.Lock()
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workerctl: worker %s not found", id)
	}
	if e.worker == nil {
		s.mu.Unlock()
		return fmt.Errorf("workerctl: worker %s is not running (nothing to snapshot)", id)
	}
	w := e.worker
	store := s.store
	s.mu.Unlock()

	if store == nil {
		return nil
	}
	snap, err := w.Snapshot()
	if err != nil {
		return fmt.Errorf("workerctl: snapshot %s: %w", id, err)
	}

	// Keep the entry's cached snapshot in step: it is what a later
	// Resume replays. Do not let a checkpoint that raced with suspend or
	// destroy resurrect a worker that is no longer running.
	s.mu.Lock()
	e = s.findLocked(id)
	if e == nil || e.worker != w || e.state != worker.StateRunning {
		s.mu.Unlock()
		return fmt.Errorf("workerctl: worker %s stopped while checkpointing", id)
	}
	e.snapshot = snap
	e.snapSeq++
	s.mu.Unlock()

	if err := store.SaveState(id, worker.StateRunning, snap); err != nil {
		return fmt.Errorf("workerctl: persist state %s: %w", id, err)
	}
	return nil
}

// Destroy stops a worker, removes it from the registry and deletes its
// persisted state.
func (s *Manager) Destroy(id string) error {
	l := s.idLock(id)
	l.Lock()
	defer l.Unlock()
	return s.destroyLocked(id)
}

// destroyLocked stops the worker first, then removes the entry and its
// persisted state. The caller holds the id lock. Stop/Close run outside s.mu.
// If Stop fails the entry is left in place and no state is deleted: a failed
// destroy must not make a still-live worker invisible.
func (s *Manager) destroyLocked(id string) error {
	s.mu.Lock()
	if s.protected[id] {
		s.mu.Unlock()
		return fmt.Errorf("workerctl: worker %s is protected and cannot be destroyed", id)
	}
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workerctl: worker %s not found", id)
	}
	w := e.worker
	ch := e.ch
	store := s.store
	s.mu.Unlock()

	if w != nil {
		if err := w.Stop(); err != nil {
			return fmt.Errorf("workerctl: stop %s: %w", id, err)
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
	log.Printf("[workerctl] destroyed worker %s", id)
	return nil
}

// Restart brings a worker up again from cfg while keeping the state it
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
func (s *Manager) Restart(ctx context.Context, cfg worker.WorkerConfig) error {
	spec, err := s.buildSpec(cfg)
	if err != nil {
		return err
	}

	l := s.idLock(spec.ID())
	l.Lock()
	defer l.Unlock()

	if !s.Has(spec.ID()) {
		return s.restoreAndRunLocked(ctx, spec, s.persistedSnapshot(spec.ID()))
	}
	if s.isProtected(spec.ID()) {
		return fmt.Errorf("workerctl: worker %s is protected and cannot be restarted", spec.ID())
	}

	// Suspend snapshots before stopping and persists the result, so the state a
	// restart has to keep is on disk before the live instance goes away.
	if err := s.suspendLocked(spec.ID()); err != nil {
		return fmt.Errorf("workerctl: restart %q: %w", spec.ID(), err)
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
		log.Printf("[workerctl] restart %s: type %s -> %s, dropping the %s snapshot", spec.ID(), prevType, spec.Type(), prevType)
		snap = nil
	}
	return s.restoreAndRunLocked(ctx, spec, snap)
}

// persistedSnapshot returns the snapshot persisted for id, or nil when there is
// none: no store, no record for that worker, or an unreadable store. It is the
// disk copy of the state a start has to replay.
func (s *Manager) persistedSnapshot(id string) []byte {
	if s.store == nil {
		return nil
	}
	recs, err := s.store.LoadAll()
	if err != nil {
		log.Printf("[workerctl] read persisted state %s: %v", id, err)
		return nil
	}
	for _, rec := range recs {
		if rec.ID == id {
			return rec.Snapshot
		}
	}
	return nil
}

// SnapshotAll snapshots every running worker and persists it, so a clean
// shutdown leaves the latest state on disk. Called on process shutdown.
func (s *Manager) SnapshotAll() {
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
			log.Printf("[workerctl] shutdown snapshot %s: %v", t.id, err)
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
				log.Printf("[workerctl] persist shutdown state %s: %v", t.id, err)
			}
		}
	}
}

// List returns info for all managed workers, optionally filtered by type.
// List returns the live instances' identity and state, optionally filtered by
// type. Placement is not known here — the control plane fills it in from the
// manifest.
func (s *Manager) List(typ string) []worker.WorkerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []worker.WorkerInfo
	for _, e := range s.entries {
		if typ != "" && e.typ != typ {
			continue
		}
		out = append(out, worker.WorkerInfo{
			ID:    e.spec.ID(),
			Type:  e.typ,
			State: string(e.state),
		})
	}
	return out
}

// Type returns the type label for the given worker ID, or false.
func (s *Manager) Type(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findLocked(id); e != nil {
		return e.typ, true
	}
	return "", false
}

// Has reports whether the given worker ID is managed by this service.
func (s *Manager) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findLocked(id) != nil
}

// Worker returns the live ManagedWorker instance for a running worker, or nil.
func (s *Manager) Instance(id string) (worker.ManagedWorker, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findLocked(id); e != nil && e.worker != nil {
		return e.worker, true
	}
	return nil, false
}

// LoadAll returns every persisted worker state record from the store.
func (s *Manager) LoadAll() ([]StateRecord, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.LoadAll()
}

// RestoreSuspended re-materializes a persisted worker as suspended — it builds
// the SpawnSpec via the registered builder but does not connect or start it.
// The snapshot from the persisted record is retained on the entry so a later
// Resume replays it (without it, a resumed worker would start empty).
// The worker is available for a later manual Resume.
func (s *Manager) RestoreSuspended(cfg worker.WorkerConfig, snapshot []byte) error {
	spec, err := s.buildSpec(cfg)
	if err != nil {
		return err
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
	log.Printf("[workerctl] restored suspended worker %s (type=%s)", spec.ID(), spec.Type())
	return nil
}

// RecoverAll restores the whole declared worker set: every config is
// re-materialized per its persisted state (running → restore+run, suspended →
// suspended). protect lists ids that may never be suspended, restarted or
// destroyed. It is the single startup entry point — the assembly only builds the
// Manager and calls this.
func (s *Manager) RecoverAll(ctx context.Context, workers []worker.WorkerConfig, protect []string) error {
	recs, err := s.LoadAll()
	if err != nil {
		return fmt.Errorf("workerctl: load all: %w", err)
	}
	byID := map[string]StateRecord{}
	for _, rec := range recs {
		byID[rec.ID] = rec
	}

	protected := map[string]bool{}
	for _, id := range protect {
		protected[id] = true
	}
	s.mu.Lock()
	s.protected = protected
	s.mu.Unlock()

	for _, cfg := range workers {
		rec, ok := byID[cfg.ID]
		if !ok {
			// Declared but never started: no persisted state yet means a
			// fresh start, not a suspended one.
			rec = StateRecord{ID: cfg.ID, State: worker.StateRunning}
		}
		if err := s.restoreOne(ctx, cfg, rec); err != nil {
			log.Printf("[workerctl] recover worker %s: %v", cfg.ID, err)
		}
	}
	return nil
}

// restoreOne materializes one worker per its persisted state.
func (s *Manager) restoreOne(ctx context.Context, cfg worker.WorkerConfig, rec StateRecord) error {
	if rec.State == worker.StateRunning {
		if len(rec.Snapshot) > 0 {
			return s.RestoreAndRun(ctx, cfg, rec.Snapshot)
		}
		_, err := s.Create(ctx, cfg)
		return err
	}
	return s.RestoreSuspended(cfg, rec.Snapshot)
}

// StopAll stops all registered workers in reverse registration order.
func (s *Manager) StopAll() {
	s.mu.Lock()
	workers := make([]worker.ManagedWorker, 0, len(s.entries))
	for _, e := range slices.Backward(s.entries) {
		if e.worker != nil {
			workers = append(workers, e.worker)
		}
	}
	s.mu.Unlock()
	for _, w := range workers {
		log.Printf("[workerctl] stopping worker %s", w.ID())
		_ = w.Stop()
	}
}

func (s *Manager) findLocked(id string) *workerEntry {
	for i := range s.entries {
		if s.entries[i].spec.ID() == id {
			return &s.entries[i]
		}
	}
	return nil
}
