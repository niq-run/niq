// Package process owns the third-party process side of the worker subsystem:
// supervising OS processes the host launches (placement managed-process), and
// provisioning the bus identity/credential for both process placements (a
// launched child and a worker that dials in on its own).
//
// It deliberately does not depend on the declaration model in the parent
// worker package. Callers lower a Manifest into the two input types below
// (Spec for supervision, IdentitySpec for provisioning), so this package can be
// tested with real child processes and a fake credential store, and the
// lifecycle backend remains its only orchestrator.
package process

import "github.com/niq-run/niq/core/itfs/event"

// Spec is everything the supervisor needs to launch and supervise one worker
// process.
type Spec struct {
	ID   string
	Type string
	// Command is the argv of the child (required).
	Command []string
	// Env is merged into the child's environment on top of the host's.
	Env map[string]string
	// Cwd is the child's working directory (empty = inherit).
	Cwd string
	// Credential is the bus token injected as NIQ_WORKER_CREDENTIAL. It is
	// minted by provisioning and never persisted for a host-launched child.
	Credential string
}

// IdentitySpec is the bus-facing input for provisioning: the worker's identity,
// and the grants to seed it with when it has no durable record yet. Empty
// grants mean the placement's default.
type IdentitySpec struct {
	ID   string
	Type string
	// Subscriptions / Publish are the broadcast/publish grants already lowered
	// to bus patterns. Empty means the placement's default.
	Subscriptions []event.EventPattern
	Publish       []event.PublishPattern
}
