//go:build e2e

package bus_e2e

import (
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/e2e/harness"
)

// TestE2ERequestReplyKeepsTheCorrelation is the request/reply contract: the
// reply is an ordinary event, paired only by the request id the request
// carried, and it comes back to the caller that sent it.
func TestE2ERequestReplyKeepsTheCorrelation(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	caller := g.DialBus(t, project, "caller")
	responder := g.DialBus(t, project, "responder")

	req := harness.NewEvent(caller.ID, "e2e.echo", map[string]any{"q": "hi"})
	req.RequestId = "req-ok"
	req.TraceID = "trace-ok"
	if err := caller.Send(t, req, responder.ID); err != nil {
		t.Fatalf("send: %v", err)
	}

	seen := responder.Await(t, "the request", 10*time.Second, withRequest("req-ok"))
	responder.Base().ReplyCompleted(seen.WorkerId, seen.RequestId, map[string]any{"answer": "pong"}, seen.TraceID)

	reply := caller.Await(t, "the correlated reply", 10*time.Second, func(evt event.Event) bool {
		return evt.RequestId == "req-ok" && evt.Type == event.TypeRequestCompleted
	})
	if reply.Payload["answer"] != "pong" {
		t.Fatalf("reply payload = %+v", reply.Payload)
	}
	if reply.TraceID != "trace-ok" {
		t.Fatalf("trace id = %q, want it propagated", reply.TraceID)
	}
}

// TestE2ERequestFailureIsAnEvent: a worker that cannot answer replies with a
// failure event carrying the message, so the caller learns why rather than
// waiting for a timeout.
func TestE2ERequestFailureIsAnEvent(t *testing.T) {
	t.Parallel()
	_, g, project := newLiveProject(t)

	caller := g.DialBus(t, project, "caller")
	responder := g.DialBus(t, project, "responder")

	req := harness.NewEvent(caller.ID, "e2e.fail", nil)
	req.RequestId = "req-fail"
	if err := caller.Send(t, req, responder.ID); err != nil {
		t.Fatalf("send: %v", err)
	}

	seen := responder.Await(t, "the request", 10*time.Second, withRequest("req-fail"))
	responder.Base().ReplyFailed(seen.WorkerId, seen.RequestId, "no such tool", seen.TraceID)

	failed := caller.Await(t, "the failure reply", 10*time.Second, func(evt event.Event) bool {
		return evt.RequestId == "req-fail" && evt.Type == event.TypeRequestFailed
	})
	if failed.Payload["error"] != "no such tool" {
		t.Fatalf("failure payload = %+v", failed.Payload)
	}
}
