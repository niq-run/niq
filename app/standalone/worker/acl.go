package worker

import (
	"fmt"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// ACLAdmin edits a worker's publish/subscribe grants: the bus identity half of a
// declaration.
//
// It is not part of the lifecycle backend because grants are identity data, not
// lifecycle state (core/itfs/bus calls its registry the control plane and offers
// Update for exactly this). What it does own is the span across two layers: the
// durable grant lives in the declaration (workers.json), the live one in the
// runtime registry, and a crash between them must leave the durable copy
// written — never a live grant that would vanish on the next start.
type ACLAdmin struct {
	ProjectID string
	Decls     DeclStore
	Registry  corebus.IdentityRegistry
}

var _ coreworker.ACLAdmin = (*ACLAdmin)(nil)

// UpdateACL replaces a worker's grants. Both lists are always written: "what this
// worker may do" is a complete statement, not a patch, so an omitted list means
// an empty grant set.
func (a *ACLAdmin) UpdateACL(id string, publish []event.PublishPattern, subscribe []event.EventPattern) error {
	if a.Decls == nil {
		return fmt.Errorf("worker %s: no declaration store", id)
	}
	d, ok, err := a.Decls.Get(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("worker %s is not declared", id)
	}

	next := *d
	next.Publish = publishSpecs(publish)
	next.Subscriptions = subscriptionSpecs(subscribe)
	if err := a.Decls.Update(&next); err != nil {
		return err
	}

	// Runtime projection last. A worker with no identity yet (declared but never
	// started) needs nothing here: registering picks the persisted grant up.
	if a.Registry == nil {
		return nil
	}
	if _, ok := a.Registry.Lookup(id); !ok {
		return nil
	}
	return a.Registry.Update(id, publish, subscribe)
}
