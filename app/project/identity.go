// Identity registration: the register-or-refresh flow shared by the in-process
// builders (specConnect) and the unmanaged-worker provisioning path. Both want
// "register this identity; if it already exists, refresh it so it reflects the
// current allow lists"; the divergence only differs in how it re-arms a
// changed credential.
package project

import (
	"strings"

	corebus "github.com/niq-run/niq/core/itfs/bus"
)

// registerIdentity registers id with the bus registry, treating a pre-existing
// identity as a re-run to refresh rather than an error. Registration is
// idempotent: an identity that already exists has its allow lists updated to
// match. If the stored credential differs from the one carried now (a newly
// rotated unmanaged-worker credential), the old identity is revoked and
// re-registered — an in-process identity always carries an empty credential, so
// this branch never fires for it.
func registerIdentity(registry corebus.IdentityRegistry, id corebus.Identity) error {
	err := registry.Register(id)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "already registered") {
		return err
	}
	existing, ok := registry.Lookup(id.WorkerID)
	if !ok {
		return err
	}
	if existing.Credential != id.Credential {
		if err := registry.Revoke(id.WorkerID); err != nil {
			return err
		}
		return registry.Register(id)
	}
	return registry.Update(id.WorkerID, id.PublishAllow, id.SubscribeAllow)
}
