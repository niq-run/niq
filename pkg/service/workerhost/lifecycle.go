// Package workerhost provides the WorkerService — the control-plane engine
// that manages worker lifecycles (register, create, suspend, resume, destroy).
//
// WorkerService owns worker construction via a per-type Builder registry
// (injected by the assembly layer) and the lifecycle state machine. It calls
// the Builder to obtain a SpawnSpec (connect/build closures), then manages
// the worker's connection, goroutine, and persisted state.
package workerhost

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/worker"
)

// Builder constructs a SpawnSpec from a serializable WorkerConfig. Builders
// are provided by the assembly layer (swarm), which knows the concrete worker
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
	snapshot []byte // last snapshot, taken at suspend time
}

// WorkerService manages worker lifecycles. It has no awareness of event types
// or subscriptions — it only knows about worker.SpawnSpec, ManagedWorker and
// lifecycle operations.
type WorkerService struct {
	entries  []workerEntry
	builders map[string]Builder
	store    WorkerStore // optional; nil disables persistence
	mu       sync.Mutex
}

// New creates an empty WorkerService.
func New() *WorkerService {
	return &WorkerService{builders: make(map[string]Builder)}
}

// SetStore attaches a persistence backend. May be nil to disable persistence.
func (s *WorkerService) SetStore(store WorkerStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

// RegisterBuilder registers a Builder for a worker type. CreateWorker dispatches
// to the registered builder for the config's Type.
func (s *WorkerService) RegisterBuilder(typ string, b Builder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.builders[typ] = b
}

// CreateWorker builds and starts a worker from a serializable config. It
// dispatches to the registered builder for cfg.Type, then spawns it.
func (s *WorkerService) CreateWorker(ctx context.Context, cfg worker.WorkerConfig) error {
	s.mu.Lock()
	b, ok := s.builders[cfg.Type]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("workerhost: no builder for type %q", cfg.Type)
	}
	spec, err := b(cfg)
	if err != nil {
		return fmt.Errorf("workerhost: build %q: %w", cfg.ID, err)
	}
	return s.spawn(ctx, spec)
}

// spawn connects, builds and starts a worker from a SpawnSpec, then records it.
func (s *WorkerService) spawn(ctx context.Context, spec worker.SpawnSpec) error {
	ch, err := spec.Connect()
	if err != nil {
		return fmt.Errorf("workerhost: connect %q: %w", spec.ID(), err)
	}
	w := spec.Build(ch)
	if err := w.Start(ctx); err != nil {
		_ = ch.Close()
		return fmt.Errorf("workerhost: start %q: %w", spec.ID(), err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findLocked(spec.ID()) != nil {
		return fmt.Errorf("workerhost: worker %s already exists", spec.ID())
	}
	s.entries = append(s.entries, workerEntry{
		spec:   spec,
		ch:     ch,
		worker: w,
		typ:    spec.Type(),
		state:  worker.StateRunning,
	})
	if s.store != nil {
		if err := s.store.SaveConfig(spec.Config); err != nil {
			log.Printf("[workerhost] persist config %s: %v", spec.ID(), err)
		}
		if err := s.store.SaveState(spec.ID(), worker.StateRunning, nil); err != nil {
			log.Printf("[workerhost] persist state %s: %v", spec.ID(), err)
		}
	}
	log.Printf("[workerhost] created worker %s (type=%s)", spec.ID(), spec.Type())
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
		return fmt.Errorf("workerhost: no builder for type %q", cfg.Type)
	}
	spec, err := b(cfg)
	if err != nil {
		return fmt.Errorf("workerhost: build %q: %w", cfg.ID, err)
	}

	ch, err := spec.Connect()
	if err != nil {
		return fmt.Errorf("workerhost: connect %q: %w", spec.ID(), err)
	}
	w := spec.Build(ch)
	if len(snapshot) > 0 {
		if err := w.Restore(snapshot); err != nil {
			_ = ch.Close()
			return fmt.Errorf("workerhost: restore %q: %w", spec.ID(), err)
		}
	}
	if err := w.Start(ctx); err != nil {
		_ = ch.Close()
		return fmt.Errorf("workerhost: start %q: %w", spec.ID(), err)
	}

	s.mu.Lock()
	if s.findLocked(spec.ID()) != nil {
		_ = w.Stop()
		_ = ch.Close()
		s.mu.Unlock()
		return fmt.Errorf("workerhost: worker %s already exists", spec.ID())
	}
	s.entries = append(s.entries, workerEntry{
		spec:   spec,
		ch:     ch,
		worker: w,
		typ:    spec.Type(),
		state:  worker.StateRunning,
	})
	s.mu.Unlock()
	if s.store != nil {
		if err := s.store.SaveConfig(spec.Config); err != nil {
			log.Printf("[workerhost] persist config %s: %v", spec.ID(), err)
		}
		if err := s.store.SaveState(spec.ID(), worker.StateRunning, snapshot); err != nil {
			log.Printf("[workerhost] persist state %s: %v", spec.ID(), err)
		}
	}
	log.Printf("[workerhost] restored worker %s (type=%s)", spec.ID(), spec.Type())
	return nil
}

// SuspendWorker stops a running worker: snapshots its state, stops its
// goroutine, and releases its bus connection. The worker's identity and
// definition are retained so it can be resumed. no-op if already suspended.
func (s *WorkerService) SuspendWorker(id string) error {
	s.mu.Lock()
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workerhost: worker %s not found", id)
	}
	if e.state == worker.StateSuspended {
		s.mu.Unlock()
		return nil
	}
	// Snapshot must happen before stopping so we capture a consistent state.
	snap, err := e.worker.Snapshot()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("workerhost: snapshot %s: %w", id, err)
	}
	if err := e.worker.Stop(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("workerhost: stop %s: %w", id, err)
	}
	if e.ch != nil {
		_ = e.ch.Close() // release the bus connection; engine marks worker offline
	}
	e.ch = nil
	e.worker = nil
	e.snapshot = snap
	e.state = worker.StateSuspended
	store := s.store
	s.mu.Unlock()

	log.Printf("[workerhost] suspended worker %s", id)
	if store != nil {
		if err := store.SaveState(id, worker.StateSuspended, snap); err != nil {
			log.Printf("[workerhost] persist state %s: %v", id, err)
		}
	}
	return nil
}

// ResumeWorker restores a suspended worker: reconnects its bus channel,
// rebuilds the instance, restores its snapshot, and starts it. no-op if running.
func (s *WorkerService) ResumeWorker(ctx context.Context, id string) error {
	s.mu.Lock()
	e := s.findLocked(id)
	if e == nil {
		s.mu.Unlock()
		return fmt.Errorf("workerhost: worker %s not found", id)
	}
	if e.state == worker.StateRunning {
		s.mu.Unlock()
		return nil
	}
	spec := e.spec
	snapshot := e.snapshot
	store := s.store
	s.mu.Unlock()

	ch, err := spec.Connect()
	if err != nil {
		return fmt.Errorf("workerhost: reconnect %q: %w", id, err)
	}
	w := spec.Build(ch)
	if snapshot != nil {
		if err := w.Restore(snapshot); err != nil {
			_ = ch.Close()
			return fmt.Errorf("workerhost: restore %q: %w", id, err)
		}
	}
	if err := w.Start(ctx); err != nil {
		_ = ch.Close()
		return fmt.Errorf("workerhost: start %q: %w", id, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findLocked(id); e != nil {
		e.ch = ch
		e.worker = w
		e.state = worker.StateRunning
	}
	log.Printf("[workerhost] resumed worker %s", id)
	if store != nil {
		if err := store.SaveState(id, worker.StateRunning, snapshot); err != nil {
			log.Printf("[workerhost] persist state %s: %v", id, err)
		}
	}
	return nil
}

// DestroyWorker stops a worker, removes it from the registry and deletes its
// persisted store. The worker's identity remains registered on the bus.
func (s *WorkerService) DestroyWorker(id string) error {
	s.mu.Lock()
	for i, e := range s.entries {
		if e.spec.ID() == id {
			if e.worker != nil {
				_ = e.worker.Stop()
			}
			if e.ch != nil {
				_ = e.ch.Close()
			}
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			store := s.store
			s.mu.Unlock()
			log.Printf("[workerhost] destroyed worker %s", id)
			if store != nil {
				_ = store.Delete(id)
			}
			return nil
		}
	}
	s.mu.Unlock()
	return fmt.Errorf("workerhost: worker %s not found", id)
}

// ShutdownSnapshot snapshots every running worker and persists it, so a clean
// shutdown leaves the latest state on disk. Called on process shutdown.
func (s *WorkerService) ShutdownSnapshot() {
	s.mu.Lock()
	store := s.store
	type target struct {
		id   string
		snap []byte
	}
	var targets []target
	for i := range s.entries {
		e := &s.entries[i]
		if e.worker == nil {
			continue
		}
		if snap, err := e.worker.Snapshot(); err == nil {
			e.snapshot = snap
			targets = append(targets, target{id: e.spec.ID(), snap: snap})
		} else {
			log.Printf("[workerhost] shutdown snapshot %s: %v", e.spec.ID(), err)
		}
	}
	s.mu.Unlock()

	if store == nil {
		return
	}
	for _, t := range targets {
		if err := store.SaveState(t.id, worker.StateRunning, t.snap); err != nil {
			log.Printf("[workerhost] persist shutdown state %s: %v", t.id, err)
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

// LoadAllWorkers returns every persisted worker record from the store.
func (s *WorkerService) LoadAllWorkers() ([]WorkerRecord, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.LoadAll()
}

// RestoreSuspended re-materializes a persisted worker as suspended — it builds
// the SpawnSpec via the registered builder but does not connect or start it.
// The worker is available for a later manual ResumeWorker.
func (s *WorkerService) RestoreSuspended(cfg worker.WorkerConfig) error {
	s.mu.Lock()
	b, ok := s.builders[cfg.Type]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("workerhost: no builder for type %q", cfg.Type)
	}
	spec, err := b(cfg)
	if err != nil {
		return fmt.Errorf("workerhost: build %q: %w", cfg.ID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findLocked(spec.ID()) != nil {
		return nil
	}
	s.entries = append(s.entries, workerEntry{
		spec:  spec,
		typ:   spec.Type(),
		state: worker.StateSuspended,
	})
	log.Printf("[workerhost] restored suspended worker %s (type=%s)", spec.ID(), spec.Type())
	return nil
}

// StartAll starts all registered workers in registration order. Workers that
// are already running are skipped.
func (s *WorkerService) StartAll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.entries {
		e := &s.entries[i]
		if e.state == worker.StateRunning || e.worker != nil {
			continue
		}
	}
	return nil
}

// StopAll stops all registered workers in reverse registration order.
func (s *WorkerService) StopAll() {
	s.mu.Lock()
	for _, e := range slices.Backward(s.entries) {
		if e.worker == nil {
			continue
		}
		log.Printf("[workerhost] stopping worker %s", e.spec.ID())
		_ = e.worker.Stop()
	}
	s.mu.Unlock()
}

// Run starts all registered workers and blocks until ctx is cancelled.
// It then stops all workers and returns ctx.Err().
func (s *WorkerService) Run(ctx context.Context) error {
	if err := s.StartAll(ctx); err != nil {
		return err
	}

	<-ctx.Done()
	log.Println("[workerhost] shutting down")
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
