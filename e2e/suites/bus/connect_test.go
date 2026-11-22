//go:build e2e

package bus_e2e

import (
	"testing"
	"time"

	"github.com/niq-run/niq/e2e/harness"
)

// TestE2EDialInReceivesWithTheIssuedCredential is the whole third-party story
// in one case: declare a worker the host does not run, take the credential the
// gateway hands out, connect over the real HTTP transport, and get traffic.
func TestE2EDialInReceivesWithTheIssuedCredential(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	declared := g.DeclareDialIn(t, project, "guest")
	if declared.Credential == "" {
		t.Fatal("no credential was issued")
	}
	peer := declared.Connect(t)

	sender := g.DialBus(t, project, "sender")
	if err := sender.Send(t, harness.NewEvent(sender.ID, "e2e.hello", map[string]any{"n": 1}), peer.ID); err != nil {
		t.Fatalf("send: %v", err)
	}

	got := peer.Await(t, "the directed event", 10*time.Second, ofType("e2e.hello"))
	if got.WorkerId != sender.ID {
		t.Fatalf("event source = %q, want %q", got.WorkerId, sender.ID)
	}
}

// TestE2EDialInRejectsAWrongCredential: the credential is the only thing
// standing between the bus and anyone who can reach the port, so a bad one must
// fail at the transport, not silently attach.
func TestE2EDialInRejectsAWrongCredential(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	declared := g.DeclareDialIn(t, project, "impostor")
	declared.Credential = declared.Credential + "x"

	if _, err := declared.TryConnect(t); err == nil {
		t.Fatal("the bus accepted a forged credential")
	}
}

// TestE2EDialInReconnectsAfterTheStreamDrops: a peer that reconnects (a laptop
// waking up, a proxy timing out) must be routable again, and the bus replaces
// the old attachment rather than delivering twice.
func TestE2EDialInReconnectsAfterTheStreamDrops(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	declared := g.DeclareDialIn(t, project, "flaky")
	first := declared.Connect(t)
	first.Close()

	second := declared.Connect(t)
	sender := g.DialBus(t, project, "sender2")
	if err := sender.Send(t, harness.NewEvent(sender.ID, "e2e.after-reconnect", nil), second.ID); err != nil {
		t.Fatalf("send after reconnect: %v", err)
	}

	second.Await(t, "delivery after reconnecting", 10*time.Second, ofType("e2e.after-reconnect"))
}
