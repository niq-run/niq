package worker

import (
	"fmt"

	corebus "github.com/niq-run/niq/core/itfs/bus"
)

// ensureIdentity makes id present in the durable identity store and projected
// onto the runtime registry. It is the connect-time entry point: a worker that
// is already declared and running must reuse the grants already on disk, while
// a first connect seeds the record with the type's default grant.
func ensureIdentity(control corebus.Control, id corebus.Identity) error {
	if control == nil {
		return fmt.Errorf("worker %s: no bus control plane", id.WorkerID)
	}
	cur, ok := control.Lookup(id.WorkerID)
	if !ok {
		return control.Register(id)
	}
	// Identity-defining fields changed (type, credential, remote flag): replace
	// the record, keeping the stored grants when the caller has none.
	if cur.Type != id.Type || cur.Credential != id.Credential || cur.Remote != id.Remote {
		if id.PublishAllow == nil && id.SubscribeAllow == nil {
			id.PublishAllow, id.SubscribeAllow = cur.PublishAllow, cur.SubscribeAllow
		}
		if err := control.Revoke(id.WorkerID); err != nil {
			return err
		}
		return control.Register(id)
	}
	// A stored grant is authoritative. Seed only when the record carries none
	// (a nil list is "never seeded"; an explicit empty list is "deny all").
	if cur.PublishAllow == nil && cur.SubscribeAllow == nil &&
		(id.PublishAllow != nil || id.SubscribeAllow != nil) {
		return control.UpdateACL(id.WorkerID, id.PublishAllow, id.SubscribeAllow)
	}
	return nil
}
