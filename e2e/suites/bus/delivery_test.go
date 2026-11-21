//go:build e2e

package bus_e2e

import (
	"testing"
	"time"

	"github.com/niq-run/niq/e2e/harness"
)

// TestE2EDirectedSendReachesOnlyItsTarget: a send names its targets, and the
// event must not leak to anyone else on the bus.
func TestE2EDirectedSendReachesOnlyItsTarget(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	sender := g.DialBus(t, project, "sender")
	target := g.DialBus(t, project, "target")
	bystander := g.DialBus(t, project, "bystander")

	if err := sender.Send(t, harness.NewEvent(sender.ID, "e2e.directed", nil), target.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	target.Await(t, "the directed event", 10*time.Second, ofType("e2e.directed"))

	// Routing for this event is done once the target has it.
	time.Sleep(settle)
	if n := countOf(bystander.Received(), "e2e.directed"); n != 0 {
		t.Fatalf("a directed event reached a third party %d time(s)", n)
	}
}

// TestE2EBroadcastReachesEverySubscriber: with no targets, delivery follows the
// subscribe grants — which for a dial-in worker default to everything.
func TestE2EBroadcastReachesEverySubscriber(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	sender := g.DialBus(t, project, "sender")
	a := g.DialBus(t, project, "peer-a")
	b := g.DialBus(t, project, "peer-b")

	if err := sender.Broadcast(t, harness.NewEvent(sender.ID, "e2e.broadcast", nil)); err != nil {
		t.Fatalf("broadcast: %v", err)
	}

	for name, peer := range map[string]*harness.BusPeer{"peer-a": a, "peer-b": b} {
		peer.Await(t, "the broadcast at "+name, 10*time.Second, ofType("e2e.broadcast"))
	}
}

// TestE2EManyEventsKeepTheirOrderPerConnection: delivery is ordered per
// receiver. A burst is the cheap way to catch a fan-out that loses or reorders
// under load.
func TestE2EManyEventsKeepTheirOrderPerConnection(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	sender := g.DialBus(t, project, "sender")
	receiver := g.DialBus(t, project, "receiver")

	const n = 50
	for i := 0; i < n; i++ {
		evt := harness.NewEvent(sender.ID, "e2e.seq", map[string]any{"i": i})
		if err := sender.Send(t, evt, receiver.ID); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	harness.WaitFor(t, "all events to arrive", 20*time.Second, func() bool {
		return countOf(receiver.Received(), "e2e.seq") == n
	})

	last := -1
	for _, evt := range receiver.Received() {
		if string(evt.Type) != "e2e.seq" {
			continue
		}
		i, _ := evt.Payload["i"].(float64)
		if int(i) != last+1 {
			t.Fatalf("out of order: got %v after %d", evt.Payload["i"], last)
		}
		last = int(i)
	}
}
