package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/baseworker"
)

// Mode controls which tools the WorkspaceWorker registers.
type Mode int

const (
	ModeFull     Mode = iota // all tools
	ModeReadOnly             // read, ls, grep, find only
	ModeSafe                 // read, write, edit, ls, grep, find (no bash)
)

// Config holds the configuration for a WorkspaceWorker.
type Config struct {
	ID      string
	Bus     corebus.WorkerSideChannel
	Backend any
	Mode    Mode
	// Approver is the worker ID boundary-expansion requests (approval.request)
	// are sent to when a tool path escapes every mount. Empty disables the
	// approval flow — escapes fail immediately.
	Approver string
	// OnDurableChange is invoked when the worker changed state that must
	// survive a restart (e.g. a runtime-added mount). Baseworker machinery;
	// nil leaves signalling off.
	OnDurableChange func()
}

// WorkspaceWorker is a worker.ManagedWorker that provides file, command,
// and directory operations. It delegates I/O to a shared Backend and
// controls tool availability through its Mode.
type WorkspaceWorker struct {
	baseworker.BaseWorker
	backend   any
	mode      Mode
	handlers  map[string]worker.ToolFunc
	started   bool
	cancelRun context.CancelFunc
	mu        sync.Mutex

	// approver receives approval.request events for boundary expansion;
	// pendingApprovals parks the tool calls awaiting a decision (keyed by the
	// approval event's ID). Guarded by pendingMu; the event loop is single,
	// so the mutex mainly serializes against Snapshot.
	approver         string
	pendingMu        sync.Mutex
	pendingApprovals map[string]*pendingApproval
}

func New(cfg Config) *WorkspaceWorker {
	w := &WorkspaceWorker{
		BaseWorker:       baseworker.NewBaseWorker(cfg.ID, cfg.Bus),
		backend:          cfg.Backend,
		mode:             cfg.Mode,
		approver:         cfg.Approver,
		pendingApprovals: make(map[string]*pendingApproval),
	}
	// Durable-change signalling is the base's machinery (baseworker); the
	// mechanism only decides when to raise it (see handleMountAdd /
	// handleModeSet / requestApproval).
	w.SetOnDurableChange(cfg.OnDurableChange)
	return w
}

// ── Lifecycle ──

func (w *WorkspaceWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("workspace %s: already started", w.ID())
	}
	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancelRun = cancelFn

	w.buildHandlers()
	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	// The durable-change signal (baseworker) delivers the persistence
	// callback off the event loop; no goroutine when nothing is installed.
	w.StartDurableLoop(runCtx)
	w.AnnounceReady("workspace", nil)
	w.started = true
	return nil
}

func (w *WorkspaceWorker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	w.cancelRun()
	w.cancelRun = nil
	w.started = false
	return nil
}

// workspaceState is the worker's durable execution state: the mount set, the
// read/write mode, and the tool calls parked awaiting approval. Config seeds
// the mounts at construction and runtime events mutate them, so restore
// unconditionally overrides config whenever the snapshot carries mounts (same
// semantics as the reason worker's Programs).
type workspaceState struct {
	Mounts           []string          `json:"mounts"`
	Mode             string            `json:"mode,omitempty"`
	PendingApprovals []pendingApproval `json:"pending_approvals,omitempty"`
}

// Snapshot captures the worker's durable execution state. State lives behind
// the backend's own mutex (mounts) and the worker's pendingMu (approvals), so
// this is safe to call from the durable goroutine while the event loop is
// running.
func (w *WorkspaceWorker) Snapshot() ([]byte, error) {
	mm, ok := w.backend.(MountManager)
	var mounts []string
	if ok {
		mounts = mm.Mounts()
	}
	w.mu.Lock()
	mode := modeName(w.mode)
	w.mu.Unlock()
	state := workspaceState{Mounts: mounts, Mode: mode, PendingApprovals: w.parkedApprovals()}
	return json.Marshal(state)
}

// Restore rehydrates the worker from a Snapshot blob, replacing the
// backend's mount set with the persisted one and re-applying the mode and
// the parked approvals.
//
//	Called after construction and before Start.
func (w *WorkspaceWorker) Restore(state []byte) error {
	var s workspaceState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("workspace restore: %w", err)
	}
	// Field-absent snapshots (nil Mounts) leave config standing; a snapshot
	// with mounts overrides it — runtime additions must survive restarts. A
	// mount that no longer exists is not fatal (mirrors the reason worker's
	// stale-provider handling): skip it and stay on what still resolves —
	// the constructor is equally lenient about config mounts.
	if s.Mounts != nil {
		if mm, ok := w.backend.(MountManager); ok {
			if err := mm.ReplaceMounts(s.Mounts); err != nil {
				log.Printf("[workspace %s] restore: %v; keeping config mounts", w.ID(), err)
			}
		} else {
			log.Printf("[workspace %s] restore: backend does not support mounts; snapshot ignored", w.ID())
		}
	}
	if s.Mode != "" {
		switch s.Mode {
		case ModeNameReadOnly:
			w.mode = ModeReadOnly
		case ModeNameReadWrite:
			w.mode = ModeFull
		default:
			log.Printf("[workspace %s] restore: unknown mode %q ignored", w.ID(), s.Mode)
		}
	}
	w.restoreParkedApprovals(s.PendingApprovals)
	log.Printf("[workspace %s] restore: %d mount(s), mode %s, %d pending approval(s)",
		w.ID(), len(s.Mounts), modeName(w.mode), len(s.PendingApprovals))
	return nil
}

// ── Event loop ──

func (w *WorkspaceWorker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(ctx, evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *WorkspaceWorker) process(ctx context.Context, evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerDiscover:
		w.AnnounceReady("workspace", nil)
	case event.TypeRequestCancel:
		callID := evt.RequestId
		log.Printf("[workspace %s] cancel requested for %s (best-effort)", w.ID(), callID)
	case event.TypeApprovalDecision:
		// A reply, not an extension invocation: resolve the parked call it
		// correlates with (approval.request's RequestId).
		w.handleApprovalDecision(ctx, evt)
	default:
		if !w.DispatchExtension(evt) {
			log.Printf("[workspace %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}
