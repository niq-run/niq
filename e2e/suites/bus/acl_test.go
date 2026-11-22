//go:build e2e

package bus_e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/niq-run/niq/e2e/harness"
)

// setAllow replaces a worker's grants through the API the UI uses.
//
// The grants are sent in the bare-string form on purpose: a publish grant is
// {type, target}, and an omitted target denies every directed send. The string
// form means "this type, any target", which is what these tests are about.
func setAllow(t *testing.T, g *harness.Gateway, project, id string, publish, subscribe []string) {
	t.Helper()
	body := map[string]any{}
	if publish != nil {
		body["publish_allow"] = publish
	}
	if subscribe != nil {
		body["subscribe_allow"] = subscribe
	}
	code, resp := g.PutJSON(t, "/p/"+project+"/api/workers/"+id+"/allow", body)
	if code != http.StatusOK {
		t.Fatalf("PUT allow %s: %d %s", id, code, resp)
	}
}

// TestE2EPublishGrantIsEnforced: a worker may publish only what its identity
// allows. The refusal is a drop downstream of the transport — /publish accepts
// the request and answers 200 before routing decides — so the observable
// contract is that the denied event never arrives, not that the call fails.
func TestE2EPublishGrantIsEnforced(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	peer := g.DialBus(t, project, "restricted")
	receiver := g.DialBus(t, project, "receiver")

	setAllow(t, g, project, peer.ID, []string{"e2e.allowed"}, nil)

	if err := peer.Send(t, harness.NewEvent(peer.ID, "e2e.denied", nil), receiver.ID); err != nil {
		t.Fatalf("the transport refused the publish: %v", err)
	}
	if err := peer.Send(t, harness.NewEvent(peer.ID, "e2e.allowed", nil), receiver.ID); err != nil {
		t.Fatalf("the allowed publish was rejected: %v", err)
	}

	// The allowed one arriving proves the send path is alive, so the absence of
	// the other is the grant at work rather than a stalled connection.
	receiver.Await(t, "the allowed event", 10*time.Second, ofType("e2e.allowed"))
	time.Sleep(settle)
	if n := countOf(receiver.Received(), "e2e.denied"); n != 0 {
		t.Fatalf("a publish outside the grant was delivered %d time(s)", n)
	}
}

// TestE2ESubscribeGrantIsEnforcedLive: narrowing the grant stops delivery
// immediately, and widening it resumes it — no reconnect, because routing reads
// the identity, not a connection-time snapshot.
func TestE2ESubscribeGrantIsEnforcedLive(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	sender := g.DialBus(t, project, "sender")
	watcher := g.DialBus(t, project, "watcher")

	// Nothing it is interested in: the broadcast must not reach it.
	setAllow(t, g, project, watcher.ID, nil, []string{"e2e.unrelated"})
	if err := sender.Broadcast(t, harness.NewEvent(sender.ID, "e2e.hidden", nil)); err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	time.Sleep(settle)
	if n := countOf(watcher.Received(), "e2e.hidden"); n != 0 {
		t.Fatalf("a narrowed grant still delivered %d time(s)", n)
	}

	// Widen it on the live connection.
	setAllow(t, g, project, watcher.ID, nil, []string{"e2e.hidden"})
	if err := sender.Broadcast(t, harness.NewEvent(sender.ID, "e2e.hidden", nil)); err != nil {
		t.Fatalf("second broadcast: %v", err)
	}
	watcher.Await(t, "the event after widening the grant", 10*time.Second, ofType("e2e.hidden"))
}
