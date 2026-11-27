// Bus identity and credential provisioning for third-party workers.
//
//   - ProvisionManagedProcess: a child the host launches. Its grants are durable
//     (they survive restarts and ACL edits), but its credential is minted per
//     launch into the temporary tier and never persisted.
//   - ProvisionUnmanagedProcess: a worker that dials in on its own. Its grants
//     and credential are both durable, so it can reconnect across restarts.
//
// Both write through the bus control plane — the single owner of identity data
// — and return the credential to hand to the child.
//
// It does not supervise anything; process lifecycle lives in supervisor.go.
package process

import (
	"fmt"
	"strings"
	"time"

	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// ── identity and credential provisioning ──

// BuildRemoteIdentity assembles the remotable identity record for a
// third-party worker. Remote is set so the transport authenticates it: this is
// a worker that is allowed (indeed expected) to connect over the HTTP bus,
// unlike an in-process worker whose id must never be claimable there.
func BuildRemoteIdentity(spec *IdentitySpec) corebus.Identity {
	subAllow := spec.Subscriptions
	if len(subAllow) == 0 {
		// A niq link's far-side identity only needs the collaborative
		// worker.input plus worker.discover (so it can be found / announced on
		// that bus). Defaulting it to `*` would ingest the WHOLE far-side bus
		// broadcast, and the link would relay (reason.start etc.) as
		// conversation. Other third-party workers keep the `*` fallback for
		// backward compatibility with broadcast-driven peers.
		if spec.Type == "niq" {
			subAllow = []event.EventPattern{
				event.NewPattern(event.TypeWorkerInput),
				event.NewPattern("worker.discover"),
			}
		} else {
			subAllow = []event.EventPattern{event.NewPattern("*")}
		}
	}
	pubAllow := spec.Publish
	if len(pubAllow) == 0 {
		// A niq link's far-side identity only publishes what the link needs on
		// that bus: forward collaborative worker.input, reply to control
		// (request.*), and announce presence. Defaulting it to `*` would let the
		// dialed-in worker publish arbitrary event types / reach any worker on
		// the far side. Other third-party workers keep the `*` fallback.
		if spec.Type == "niq" {
			pubAllow = []event.PublishPattern{
				event.NewPublishPattern("worker.input"),
				event.NewPublishPattern("request.*"),
				event.NewPublishPattern("worker.ready"),
			}
		} else {
			pubAllow = []event.PublishPattern{event.NewPublishPattern("*")}
		}
	}
	return corebus.Identity{
		WorkerID:       spec.ID,
		Type:           spec.Type,
		PublishAllow:   pubAllow,
		SubscribeAllow: subAllow,
		Remote:         true,
	}
}

// persistRemoteTokenTTL is the DEFAULT lifetime of a provisioned remote
// identity's signed token (180 days). A provisioned remote worker reuses the
// persisted token across bus restarts, so the cap forces periodic
// re-provisioning rather than infinitely-valid credentials.
const persistRemoteTokenTTL = 180 * 24 * time.Hour

// tempTokenTTL is the fixed backstop lifetime of a managed-process worker's
// token. The credential is in-memory only and dies with the process; the TTL is
// deliberately NOT configurable — its real lifetime is the child's lifetime.
const tempTokenTTL = 30 * 24 * time.Hour

// ProvisionManagedProcess provisions a child the host itself launches. The
// durable identity record carries the grants (so an ACL edit survives a
// restart) but no credential; a fresh token is minted per launch and registered
// in the temporary tier, which is where the transport authenticates it. The
// token is returned for the child's environment.
func ProvisionManagedProcess(registry corebus.IdentityRegistry, signer *eventbus.TokenSigner, control corebus.Control, spec IdentitySpec) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("provision %s: no token signer configured", spec.ID)
	}
	if control == nil {
		return "", fmt.Errorf("provision %s: no bus control plane", spec.ID)
	}
	idt, ok := control.Lookup(spec.ID)
	switch {
	case !ok:
		idt = BuildRemoteIdentity(&spec)
		idt.Credential = "" // never persisted
		if err := control.Register(idt); err != nil {
			return "", err
		}
	case idt.PublishAllow == nil && idt.SubscribeAllow == nil:
		// A migrated/legacy record with no grants yet: seed the type default.
		seed := BuildRemoteIdentity(&spec)
		if err := control.UpdateACL(spec.ID, seed.PublishAllow, seed.SubscribeAllow); err != nil {
			return "", err
		}
		idt.PublishAllow, idt.SubscribeAllow = seed.PublishAllow, seed.SubscribeAllow
	}
	tok, err := signer.Mint(spec.ID, tempTokenTTL)
	if err != nil {
		return "", err
	}
	idt.Credential = tok
	if err := RegisterTempIdentity(registry, idt); err != nil {
		return "", err
	}
	return tok, nil
}

// ProvisionUnmanagedProcess provisions a remotely-connected worker — one the
// host did NOT launch (a third party dials in on its own) — with a durable
// identity and a long-lived signed token. The first provisioning mints the
// token and persists it through the bus control plane; later runs reuse it (the
// bus verifies the token by signature, and the signing secret also persists).
func ProvisionUnmanagedProcess(control corebus.Control, signer *eventbus.TokenSigner, spec IdentitySpec) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("provision %s: no token signer configured", spec.ID)
	}
	if control == nil {
		return "", fmt.Errorf("provision %s: no bus control plane", spec.ID)
	}
	cur, ok := control.Lookup(spec.ID)
	if !ok {
		idt := BuildRemoteIdentity(&spec)
		tok, err := signer.Mint(spec.ID, persistRemoteTokenTTL)
		if err != nil {
			return "", err
		}
		idt.Credential = tok
		if err := control.Register(idt); err != nil {
			return "", err
		}
		return tok, nil
	}
	// A stored grant is authoritative; only an unseeded record takes the
	// caller's grant.
	if cur.PublishAllow == nil && cur.SubscribeAllow == nil && (spec.Publish != nil || spec.Subscriptions != nil) {
		if err := control.UpdateACL(spec.ID, spec.Publish, spec.Subscriptions); err != nil {
			return "", err
		}
		cur.PublishAllow, cur.SubscribeAllow = spec.Publish, spec.Subscriptions
	}
	if cur.Credential != "" {
		// The durable record may exist while the runtime projection was dropped
		// (an archived worker being restored): make it routable again.
		if err := control.Project(spec.ID); err != nil {
			return "", err
		}
		return cur.Credential, nil
	}
	tok, err := signer.Mint(spec.ID, persistRemoteTokenTTL)
	if err != nil {
		return "", err
	}
	if err := control.SetCredential(spec.ID, tok); err != nil {
		return "", err
	}
	return tok, nil
}

// RegisterTempIdentity registers a temporary (process-lifetime) identity. It
// prefers the in-memory tier of a LayeredRegistry so the id dies with the main
// process; if the registry has no temp tier it falls back to a durable
// registration so an unlayered registry still works in isolation. A stale temp
// entry for the same id (from a prior provision before a respawn) is replaced
// so re-provisioning is idempotent.
func RegisterTempIdentity(registry corebus.IdentityRegistry, id corebus.Identity) error {
	if lyr, ok := registry.(*eventbus.LayeredRegistry); ok {
		if err := lyr.RegisterTemp(id); err == nil {
			return nil
		}
		lyr.RevokeTemp(id.WorkerID)
		return lyr.RegisterTemp(id)
	}
	return RegisterIdentity(registry, id)
}

// RegisterIdentity registers id with a plain identity registry, treating a
// pre-existing identity as a re-run to refresh rather than an error. Used by
// tests and unlayered registries; production goes through the bus control
// plane.
func RegisterIdentity(registry corebus.IdentityRegistry, id corebus.Identity) error {
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
