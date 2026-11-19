package bus

import "github.com/niq-run/niq/core/itfs/event"

// Control is the durable identity control plane. It owns the identities that
// must survive a restart — managed workers, remotely-connected peers — and the
// grants attached to them. It is deliberately separate from the worker
// lifecycle: one answers "who may say what on the bus", the other "which
// worker exists and is running".
//
// Every mutation is durable first and projected onto the runtime registry
// second, so a crash can only leave a grant that is already on disk.
type Control interface {
	// Register records a new identity. An id that already exists is an error:
	// identity ownership is explicit, never implicit.
	Register(id Identity) error
	// UpdateACL replaces an identity's publish/subscribe grants. Both lists are
	// written: "what this worker may do" is a complete statement, not a patch.
	// An unknown id is an error.
	UpdateACL(workerID string, publish []event.PublishPattern, subscribe []event.EventPattern) error
	// SetCredential records the credential minted for an existing identity.
	// Provisioning needs it: the identity is registered first (grants, remote
	// flag), then its token is generated. An unknown id is an error.
	SetCredential(workerID, credential string) error
	// Revoke removes an identity. Revoking an unknown id is a no-op, so a
	// destroy path can call it unconditionally.
	Revoke(workerID string) error
	// Lookup returns one identity from the durable store.
	Lookup(workerID string) (Identity, bool)
	// List returns every durable identity, sorted by worker id.
	List() []Identity
}

// Store is the durable identity boundary a Control implementation writes
// through. The deployment form injects it: a JSON file for standalone, a shared
// key-value store for cluster. Implementations must be safe for concurrent use.
type Store interface {
	Load(id string) (Identity, bool, error)
	Save(id string, idt Identity) error
	Delete(id string) error
	All() ([]Identity, error)
}
