package worker

import (
	"context"
)

// Placement is where a worker runs and who owns its lifecycle. It replaces the
// old managed / external / remote spelling, which conflated two questions:
// "does the host manage it" and "does it run in the host process". The first
// two placements below are both host-managed; only the last one is not.
type Placement string

const (
	// PlacementInProcess is a worker built by a type builder and running inside
	// the host process. The host owns the full lifecycle: create, suspend
	// (snapshot + stop), resume, restart and destroy.
	PlacementInProcess Placement = "in-process"

	// PlacementManagedProcess is a third-party OS process the host launches and
	// supervises: crash restart with backoff, start/stop/restart, group kill on
	// shutdown. Durability of what happens inside the process belongs to the
	// third party (it receives a persistent NIQ_STATE_DIR).
	PlacementManagedProcess Placement = "managed-process"

	// PlacementUnmanagedProcess is a third-party worker that starts itself and
	// dials the bus. The host only declares it and issues (or reuses) its bus
	// identity and credential; it launches and supervises nothing.
	PlacementUnmanagedProcess Placement = "unmanaged-process"
)

// WorkerSpec is the input of Backend.Create: everything needed to bring a
// worker into existence, in either placement. Config carries the identity,
// display metadata and type-specific construction params; the remaining fields
// are the launch spec of a managed-process worker (ignored otherwise).
//
// PublishAllow / SubscribeAllow are a SEED for the worker's bus identity, not
// part of its manifest: the bus control plane owns the durable grants, and an
// identity that already exists keeps them.
type WorkerSpec struct {
	WorkerConfig
	// Placement defaults to PlacementInProcess when empty.
	Placement Placement `json:"placement,omitempty"`
	// Command is the argv of a managed-process worker (required for that
	// placement, rejected for the others).
	Command []string `json:"command,omitempty"`
	// Cwd is the child's working directory for a managed-process worker.
	Cwd string `json:"cwd,omitempty"`
}

// WorkerPatch is a partial edit of the DECLARED half of a worker: what it is
// called, how it is built. Every field is optional; a nil pointer, nil slice or
// nil map means "leave unchanged", while an empty slice or empty map means
// "clear".
//
// ACL is deliberately absent. Publish/subscribe grants are bus identity data,
// owned by the bus control plane — editing them belongs to the bus, not to a
// worker's lifecycle backend.
type WorkerPatch struct {
	Label       *string        `json:"label,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Description *string        `json:"description,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
}

// WorkerInfo is the single read view of a worker: identity, where it runs, the
// state its owner reports, and the display metadata a UI renders. There is no
// second "declaration" view — a display layer reads the same record the
// lifecycle returns.
type WorkerInfo struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Placement Placement `json:"placement"`
	// State is "running", "suspended" or "stopped" (declared, not instantiated).
	State string `json:"state"`
	// Archived reports which set the worker belongs to. The ACTIVE views return
	// false; List(true) returns the archived set with true.
	Archived    bool           `json:"archived,omitempty"`
	Label       string         `json:"label,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Description string         `json:"description,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
}

// Lifecycle is the runtime half: the operations that change whether a worker
// exists or is running.
type Lifecycle interface {
	// Create brings a new worker into existence in the spec's placement and
	// records its manifest. It returns the worker as resolved by its builder (an
	// in-process builder may derive an id, e.g. a workspace from its path),
	// which is the identity the bus and the manifest use.
	Create(ctx context.Context, spec WorkerSpec) (WorkerInfo, error)
	// Suspend stops a running worker. For an in-process worker the state is
	// snapshotted first; a managed-process worker is stopped and its successor
	// replays whatever the third party persisted itself.
	Suspend(ctx context.Context, id string) error
	// Resume starts a worker that is declared but not running — a suspended
	// in-process worker resumes from its snapshot, a managed-process worker is
	// relaunched, an unmanaged one gets its identity re-ensured.
	Resume(ctx context.Context, id string) error
	// Restart rebuilds a worker from its manifest: an in-process worker keeps
	// the state it owns (its snapshot), a managed-process worker is relaunched.
	Restart(ctx context.Context, id string) error
	// Destroy stops a worker and removes every trace of it: manifest, persisted
	// state, bus connection and identity.
	Destroy(ctx context.Context, id string) error
	// Archive moves a worker in or out of the archived set. Archiving suspends
	// it if live and drops its runtime identity but keeps the manifest (and the
	// durable identity) so it can be restored unchanged.
	Archive(ctx context.Context, id string, archived bool) error
	// Protected reports whether id may never be suspended, restarted, archived
	// or destroyed (the host itself, the WebUI's interface worker, whatever the
	// assembly marks essential). It lets the host refuse such a request up front
	// instead of handing it to the backend.
	Protected(id string) bool
}

// Manifest is the declaration half: read views plus the one edit that is not a
// lifecycle step.
type Manifest interface {
	// Get returns one active worker. An archived worker reports ok=false.
	Get(id string) (WorkerInfo, bool, error)
	// List returns the manifests of one set. archived=false is the active set,
	// archived=true the archived set.
	List(archived bool) []WorkerInfo
	// Update applies a partial edit to the declared half.
	Update(ctx context.Context, id string, patch WorkerPatch) (WorkerInfo, error)
}

// Backend is the complete control surface for one host's workers: lifecycle
// plus manifest. It is the only interface the host worker depends on, so a
// different deployment form (another placement set, a distributed scheduler)
// changes the backend and leaves the host untouched.
//
// Recovery is NOT here: restoring the declared set is the control plane's own
// startup step, and process liveness belongs to the assembly.
type Backend interface {
	Lifecycle
	Manifest
}
