package worker

import "context"

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

// WorkerInfo is a read-only view of one worker's lifecycle: what it is, where
// it runs, and the state its owner reports.
type WorkerInfo struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Placement Placement `json:"placement"`
	// State is "running", "suspended" or "stopped" (declared, not instantiated).
	State string `json:"state"`
}

// Lifecycle is the control surface the host worker needs. It has no notion of
// event types or declarations — only of workers and their lifecycle.
type Lifecycle interface {
	// Create brings a new worker into existence in the spec's placement and
	// records its declaration. It returns the worker as resolved by its builder
	// (an in-process builder may derive an id, e.g. a workspace from its path),
	// which is the identity the bus and the declaration use.
	Create(ctx context.Context, spec WorkerSpec) (WorkerInfo, error)
	// Suspend stops a running worker. For an in-process worker the state is
	// snapshotted first; a managed-process worker is stopped and its successor
	// replays whatever the third party persisted itself.
	Suspend(ctx context.Context, id string) error
	// Resume starts a worker that is declared but not running — a suspended
	// in-process worker resumes from its snapshot, a managed-process worker is
	// relaunched, an unmanaged one gets its identity re-ensured.
	Resume(ctx context.Context, id string) error
	// Restart rebuilds a worker from its declaration: an in-process worker keeps
	// the state it owns (its snapshot), a managed-process worker is relaunched.
	Restart(ctx context.Context, id string) error
	// Destroy stops a worker and removes every trace of it: declaration,
	// persisted state, bus connection and identity.
	Destroy(ctx context.Context, id string) error
	// Protected reports whether id may never be suspended, restarted or
	// destroyed (the host itself, the WebUI's HIW, whatever the assembly marks
	// essential). It lets the host refuse such a request up front — notably
	// before it raises an approval for something that could never be applied.
	Protected(id string) bool
}

// Catalog is the read-only half: what the backend knows, regardless of whether
// the worker is live. It answers lifecycle questions only — which workers
// exist and what state they are in. Capability discovery (who serves which
// tools) belongs to the directory worker.
type Catalog interface {
	Get(id string) (WorkerInfo, bool, error)
	List() []WorkerInfo
}

// RecoverOptions parameterizes Backend.Recover: the declared worker set to
// materialize, the workers that must exist and run, the ones that must be
// suspended, and the ids that may never be suspended or destroyed.
type RecoverOptions struct {
	// Declared is the declaration-sourced worker set. The persisted state store
	// only holds runtime state, so the definition comes from here.
	Declared []WorkerSpec
	// Essential workers must exist and run: created fresh when absent,
	// restored to running even when persisted suspended.
	Essential []WorkerSpec
	// Suspend lists ids that must be suspended regardless of persisted state.
	Suspend []string
	// Protect lists ids that are protected from suspend/restart/destroy even
	// when they are not Essential (the host itself, the WebUI's HIW).
	Protect []string
}

// Backend owns worker lifecycles for one host: it creates, stops, resumes,
// restarts and destroys workers in every placement, and it recovers the
// declared set at startup. Implementations live in the assembly layer, which
// knows where declarations, identities and processes live.
type Backend interface {
	Lifecycle
	Catalog
	// Recover materializes the declared set and applies the essential /
	// suspend / protect overrides.
	Recover(ctx context.Context, opts RecoverOptions) error
	// Run blocks until ctx is done, then releases every worker it owns.
	Run(ctx context.Context) error
}
