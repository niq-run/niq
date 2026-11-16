// Bus identity and credential provisioning for third-party workers.
//
//   - ProvisionManagedProcess: a short-lived credential + in-memory identity for
//     a child the host launches (placement managed-process).
//   - ProvisionUnmanagedProcess: a durable credential + identity for a worker
//     that dials in on its own (placement unmanaged-process).
//
// It does not supervise anything; process lifecycle lives in supervisor.go.
package process

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// ── identity and credential provisioning ──

// BuildRemoteIdentity assembles the remotable identity record for a
// third-party worker from its declaration. Remote is set so the transport
// authenticates it: this is a worker that is allowed (indeed expected) to
// connect over the HTTP bus, unlike an in-process worker whose id must never be
// claimable there.
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
		Credential:     spec.Credential,
		Remote:         true,
	}
}

// persistRemoteTokenTTL is the DEFAULT lifetime of a provisioned remote
// identity's signed token (180 days). A provisioned remote worker reuses the
// persisted token across bus restarts, so the cap forces periodic
// re-provisioning rather than infinitely-valid credentials. The expiry can be
// overridden per-worker via Declaration.TokenTTLSeconds.
const persistRemoteTokenTTL = 180 * 24 * time.Hour

// tempTokenTTL is the fixed backstop lifetime of a managed-process worker's
// token. The credential is in-memory only and dies with the process; the TTL is
// deliberately NOT configurable — its real lifetime is the child's lifetime.
const tempTokenTTL = 30 * 24 * time.Hour

// ttlFor resolves a worker's token lifetime: an explicit TokenTTLSeconds wins,
// otherwise the default for the provisioning class.
func ttlFor(spec *IdentitySpec, def time.Duration) time.Duration {
	if spec != nil && spec.TokenTTLSeconds != nil && *spec.TokenTTLSeconds > 0 {
		return time.Duration(*spec.TokenTTLSeconds) * time.Second
	}
	return def
}

// ProvisionManagedProcess provisions a worker process the host itself
// launches: a short-lived signed token and an identity that lives in the
// in-memory tier of a LayeredRegistry for the duration of the main process.
// Nothing is persisted to project.json. The token is handed to the spawned
// child via its environment (NIQ_WORKER_CREDENTIAL) and dies when the process
// (or the identity's memory tier) goes away.
func ProvisionManagedProcess(registry corebus.IdentityRegistry, signer *eventbus.TokenSigner, spec *IdentitySpec) error {
	if signer == nil {
		return fmt.Errorf("provision %s: no token signer configured", spec.ID)
	}
	tok, err := signer.Mint(spec.ID, tempTokenTTL)
	if err != nil {
		return err
	}
	spec.Credential = tok
	return RegisterTempIdentity(registry, BuildRemoteIdentity(spec))
}

// ProvisionUnmanagedProcess provisions a remotely-connected worker — one the
// host did NOT launch (a third party dials in on its own) — with a durable
// identity and a long-lived signed token. First provisioning mints the token
// and persists it to the declaration; later runs reuse it (the bus verifies
// the token by signature, and the signing secret also persists). The identity
// (Remote=true) is registered durably so it survives restarts.
func ProvisionUnmanagedProcess(registry corebus.IdentityRegistry, signer *eventbus.TokenSigner, store CredentialStore, spec *IdentitySpec) error {
	if signer == nil {
		return fmt.Errorf("provision %s: no token signer configured", spec.ID)
	}
	if spec.Credential == "" {
		tok, err := signer.Mint(spec.ID, ttlFor(spec, persistRemoteTokenTTL))
		if err != nil {
			return err
		}
		spec.Credential = tok
		if store != nil {
			if err := store.SetCredential(spec.ID, spec.Credential); err != nil {
				return err
			}
		}
	}
	return RegisterIdentity(registry, BuildRemoteIdentity(spec))
}

// RegisterIdentity registers id with the bus registry, treating a pre-existing
// identity as a re-run to refresh rather than an error. Registration is
// idempotent: an identity that already exists has its allow lists updated to
// match. If the stored credential differs from the one carried now (a newly
// rotated external-worker credential), the old identity is revoked and
// re-registered — an in-process identity always carries an empty credential, so
// this branch never fires for it.
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

// ResolveSecret resolves a credential that may be a literal value or an
// "env:VAR" / "file:PATH" reference. References keep remote credentials out of
// the declaration; a bare value passes through unchanged.
func ResolveSecret(s string) string {
	if s == "" {
		return s
	}
	if v, ok := strings.CutPrefix(s, "env:"); ok {
		return os.Getenv(v)
	}
	if v, ok := strings.CutPrefix(s, "file:"); ok {
		if b, err := os.ReadFile(v); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return s
}
