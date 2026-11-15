package worker

import (
	"context"

	"github.com/niq-run/niq/core/itfs/event"
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
type WorkerSpec struct {
	WorkerConfig
	// Placement defaults to PlacementInProcess when empty.
	Placement Placement `json:"placement,omitempty"`
	// Command is the argv of a managed-process worker (required for that
	// placement, rejected for the others).
	Command []string `json:"command,omitempty"`
	// Cwd is the child's working directory for a managed-process worker.
	Cwd string `json:"cwd,omitempty"`
	// TokenTTLSeconds overrides the lifetime of a provisioned remote
	// (unmanaged-process) credential. Only that placement persists a credential
	// across restarts; a managed-process credential's lifetime is its child
	// process, and an in-process worker never authenticates over the wire.
	TokenTTLSeconds *int64 `json:"token_ttl_seconds,omitempty"`
}

// WorkerPatch is a partial edit of the DECLARED half of a worker: what it is
// called, how it is built. Every field is optional; a nil pointer, nil slice or
// nil map means "leave unchanged", while an empty slice or empty map means
// "clear".
//
// ACL is deliberately absent. Publish/subscribe grants are bus identity data —
// core/itfs/bus calls its registry the control plane and already offers
// IdentityRegistry.Update — so editing them belongs to the bus, not to a
// worker's lifecycle backend.
type WorkerPatch struct {
	Label       *string        `json:"label,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Description *string        `json:"description,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
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
	// essential). It lets the host refuse such a request up front instead of
	// handing it to the backend.
	Protected(id string) bool
}

// Editor edits the declaration of an existing worker. It changes nothing about
// a running instance: construction params are read at build time, so a params
// edit applies on the next start, while display metadata is cosmetic.
type Editor interface {
	// Update applies a partial edit and returns the worker's view after it.
	Update(ctx context.Context, id string, patch WorkerPatch) (WorkerInfo, error)
}

// Archiver moves a worker in and out of the archived set. An archived worker
// keeps its declaration (so it can be restored unchanged) but takes no part in
// the running swarm: it is suspended if it was live, and its bus identity is
// dropped.
type Archiver interface {
	Archive(ctx context.Context, id string, archived bool) error
}

// Decl is the read-only, core-neutral view of a worker's declaration: what it
// is called and how it is built. It exists so a display layer (the WebUI) can
// render labels and params without knowing where declarations are stored or what
// the project's declaration model looks like.
type Decl struct {
	Label       string         `json:"label,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Description string         `json:"description,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
}

// Catalog is the read-only half: what the backend knows, regardless of whether
// the worker is live. It answers lifecycle questions only — which workers
// exist, what state they are in, and what they are declared to be. Capability
// discovery (who serves which tools) belongs to the directory worker.
type Catalog interface {
	Get(id string) (WorkerInfo, bool, error)
	// List returns the active (non-archived) declared workers.
	List() []WorkerInfo
	// Archived returns the ids that are archived: declared and restorable, but
	// not part of the active set. It is separate from List so ordinary roster
	// and discovery views never have to filter them out.
	Archived() []string
	// Declarations returns the ACTIVE declarations keyed by worker id, for views
	// that render labels and construction params. Archived workers are absent
	// (they are not part of the active set); read their ids from Archived.
	Declarations() map[string]Decl
}

// ACLAdmin edits a worker's bus identity: the publish/subscribe grants that say
// what it may send and hear.
//
// It is deliberately NOT part of Backend. Grants are identity data, not
// lifecycle state — core/itfs/bus already calls its identity registry the
// control plane and offers Update for exactly this — and the durable half lives
// in the declaration. An implementation therefore spans two layers (persist the
// declaration, then apply to the runtime registry) and owns that ordering.
type ACLAdmin interface {
	UpdateACL(id string, publish []event.PublishPattern, subscribe []event.EventPattern) error
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
// restarts, archives and destroys workers in every placement, edits their
// declarations, and recovers the declared set at startup. Implementations live
// in the assembly layer, which knows where declarations, identities and
// processes live.
type Backend interface {
	Lifecycle
	Editor
	Archiver
	Catalog
	// Recover materializes the declared set and applies the essential /
	// suspend / protect overrides.
	Recover(ctx context.Context, opts RecoverOptions) error
	// Run blocks until ctx is done, then releases every worker it owns.
	Run(ctx context.Context) error
}
