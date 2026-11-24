package builtin

import (
	"sort"
	"sync"
	"testing"

	"github.com/niq-run/niq/core/impl/busctl"
	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

func testIdentityControl(t *testing.T) (*busctl.Manager, *eventbus.LayeredRegistry) {
	t.Helper()
	runtime := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
	return busctl.New(newMemIdentityStore(), runtime), runtime
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
	if err := EnsureIdentity(control, seed); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if id, ok := runtime.Lookup("w1"); !ok || len(id.PublishAllow) != 1 {
		t.Fatalf("runtime = %+v (ok=%v)", id, ok)
	}

	// The user edits the grant; a reconnect must keep it.
	if err := control.UpdateACL("w1", []event.PublishPattern{event.NewPublishPattern("worker.ready")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIdentity(control, seed); err != nil {
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
	if err := EnsureIdentity(control, corebus.Identity{
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
	if err := EnsureIdentity(control, corebus.Identity{WorkerID: "w1", Type: "reason"}); err != nil {
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

// memIdentityStore is a test-only bus.Store: identity persistence is exercised
// by the project's file store, so these tests only need a map.
type memIdentityStore struct {
	mu  sync.RWMutex
	ids map[string]corebus.Identity
}

func newMemIdentityStore() *memIdentityStore {
	return &memIdentityStore{ids: map[string]corebus.Identity{}}
}

func (s *memIdentityStore) Load(id string) (corebus.Identity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity, ok := s.ids[id]
	return identity, ok, nil
}

func (s *memIdentityStore) Save(id string, identity corebus.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = identity
	return nil
}

func (s *memIdentityStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
	return nil
}

func (s *memIdentityStore) All() ([]corebus.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]corebus.Identity, 0, len(s.ids))
	for _, identity := range s.ids {
		out = append(out, identity)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}
