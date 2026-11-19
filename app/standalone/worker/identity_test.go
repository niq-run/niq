package worker

import (
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/impl/busctl"
	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

func testIdentityControl(t *testing.T) (*busctl.ControlPlane, *eventbus.LayeredRegistry) {
	t.Helper()
	store, err := busctl.NewFileStore(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	runtime := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
	return busctl.New(store, runtime), runtime
}

// TestEnsureIdentitySeedsOnlyOnce verifies the connect-time rule: the first
// connect seeds the caller's (type default) grant, and a later connect never
// overwrites what is on disk — that is the user's edited grant.
func TestEnsureIdentitySeedsOnlyOnce(t *testing.T) {
	control, runtime := testIdentityControl(t)
	seed := corebus.Identity{
		WorkerID:       "w1",
		Type:           "reason",
		PublishAllow:   []event.PublishPattern{event.NewPublishPattern("request.*")},
		SubscribeAllow: []event.EventPattern{event.NewPattern("worker.discover")},
	}
	if err := ensureIdentity(control, seed); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if id, ok := runtime.Lookup("w1"); !ok || len(id.PublishAllow) != 1 {
		t.Fatalf("runtime = %+v (ok=%v)", id, ok)
	}

	// The user edits the grant; a reconnect must keep it.
	if err := control.UpdateACL("w1", []event.PublishPattern{event.NewPublishPattern("worker.ready")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := ensureIdentity(control, seed); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	id, _ := control.Lookup("w1")
	if len(id.PublishAllow) != 1 || id.PublishAllow[0].Type != "worker.ready" {
		t.Fatalf("edited grant was overwritten: %+v", id.PublishAllow)
	}
}

// TestEnsureIdentityKeepsExplicitDeny verifies "deny all" is not mistaken for
// "never seeded": an explicitly empty grant survives a reconnect.
func TestEnsureIdentityKeepsExplicitDeny(t *testing.T) {
	control, _ := testIdentityControl(t)
	if err := control.Register(corebus.Identity{
		WorkerID:       "w1",
		Type:           "reason",
		PublishAllow:   []event.PublishPattern{},
		SubscribeAllow: []event.EventPattern{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureIdentity(control, corebus.Identity{
		WorkerID:       "w1",
		Type:           "reason",
		PublishAllow:   []event.PublishPattern{event.NewPublishPattern("*")},
		SubscribeAllow: []event.EventPattern{event.NewPattern("*")},
	}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	id, _ := control.Lookup("w1")
	if len(id.PublishAllow) != 0 || len(id.SubscribeAllow) != 0 {
		t.Fatalf("explicit deny was re-seeded: %+v", id)
	}
}

// TestEnsureIdentityReplacesOnTypeChange verifies identity-defining fields are
// replaced while the stored grants are carried over.
func TestEnsureIdentityReplacesOnTypeChange(t *testing.T) {
	control, runtime := testIdentityControl(t)
	if err := control.Register(corebus.Identity{
		WorkerID:       "w1",
		Type:           "timer",
		PublishAllow:   []event.PublishPattern{event.NewPublishPattern("timer.timeout")},
		SubscribeAllow: []event.EventPattern{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureIdentity(control, corebus.Identity{WorkerID: "w1", Type: "reason"}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	id, _ := control.Lookup("w1")
	if id.Type != "reason" {
		t.Fatalf("type = %q, want reason", id.Type)
	}
	if len(id.PublishAllow) != 1 || id.PublishAllow[0].Type != "timer.timeout" {
		t.Fatalf("grants lost on replace: %+v", id.PublishAllow)
	}
	if live, ok := runtime.Lookup("w1"); !ok || live.Type != "reason" {
		t.Fatalf("runtime = %+v (ok=%v)", live, ok)
	}
}
