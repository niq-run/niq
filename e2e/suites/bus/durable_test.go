//go:build e2e

package bus_e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/e2e/harness"
)

// TestE2EEventsAreQueryableByRequestID: the durable log answers "what happened
// for this request", which is how a UI reconstructs a turn after a reload.
func TestE2EEventsAreQueryableByRequestID(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	sender := g.DialBus(t, project, "sender")
	receiver := g.DialBus(t, project, "receiver")

	evt := harness.NewEvent(sender.ID, "e2e.recorded", map[string]any{"k": "v"})
	evt.RequestId = "rec-1"
	if err := sender.Send(t, evt, receiver.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	receiver.Await(t, "the event", 10*time.Second, withRequest("rec-1"))

	code, body := g.Get(t, "/p/"+project+"/api/events/by-request/rec-1")
	if code != 200 {
		t.Fatalf("by-request = %d %s", code, body)
	}
	if !strings.Contains(body, "e2e.recorded") {
		t.Fatalf("the durable log does not have the event: %s", body)
	}
}

// TestE2ETransientEventsAreDeliveredButNotStored: transient events exist to be
// seen live (streaming deltas). Storing them would turn the history into a
// transcript of every partial token.
func TestE2ETransientEventsAreDeliveredButNotStored(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	sender := g.DialBus(t, project, "sender")
	receiver := g.DialBus(t, project, "receiver")

	evt := harness.NewEvent(sender.ID, "e2e.transient", nil)
	evt.RequestId = "rec-transient"
	evt.Transient = true
	if err := sender.Send(t, evt, receiver.ID); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Delivered live…
	receiver.Await(t, "the transient event", 10*time.Second, withRequest("rec-transient"))

	// …but absent from the durable log.
	code, body := g.Get(t, "/p/"+project+"/api/events/by-request/rec-transient")
	if code != 200 {
		t.Fatalf("by-request = %d %s", code, body)
	}
	if strings.TrimSpace(body) != "[]" {
		t.Fatalf("a transient event was persisted: %s", body)
	}
}

// TestE2EStreamSeesWhatTheParticipantsPublish ties the data plane to the path a
// UI reads: the same event a peer publishes shows up on the project's SSE feed,
// through the gateway's proxy.
func TestE2EStreamSeesWhatTheParticipantsPublish(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	stream := g.Watch(t, project, "type=e2e.streamed")
	sender := g.DialBus(t, project, "sender")

	if err := sender.Broadcast(t, harness.NewEvent(sender.ID, "e2e.streamed", map[string]any{"n": 7})); err != nil {
		t.Fatalf("broadcast: %v", err)
	}

	got := stream.Await(t, "the event on the stream", 10*time.Second, ofType("e2e.streamed"))
	if got.Payload["n"] != float64(7) {
		t.Fatalf("stream payload = %+v", got.Payload)
	}
}
