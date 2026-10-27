package hiw

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestUnservedTrafficIsDrained verifies HIW never answers traffic it does not
// serve: most importantly a request.completed response to an event HIW
// published (it carries a request id), which must not be answered with
// request.failed.
func TestUnservedTrafficIsDrained(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})

	resp := event.New(event.TypeRequestCompleted, "ws-1", map[string]any{"result": "ok"})
	resp.RequestId = "call-1"
	if w.dispatchRegistered(resp) {
		t.Fatal("unserved traffic was dispatched")
	}
	if len(ch.sent) != 0 {
		t.Fatalf("drained traffic got a reply: %+v", ch.sent)
	}

	read := event.New(event.TypeWorkerReadme, "peer", nil)
	read.RequestId = "r1"
	if !w.dispatchRegistered(read) {
		t.Fatal("worker.readme is not dispatched")
	}
}

// TestReadmeTextDescribesApprovals verifies the guide explains the role and
// reports the live approval counts.
func TestReadmeTextDescribesApprovals(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})
	w.recordApprovalRequest(approvalRequestEvent())

	text := w.readmeText()
	for _, want := range []string{
		"# hiw — the human on the bus",
		"worker.readme",
		"approval.request",
		"approval.decision",
		"- pending approvals: 1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("readme missing %q:\n%s", want, text)
		}
	}
}

// TestReadmeIsServedAndAnnounced verifies worker.readme is answered, and that
// the capability rides HIW's worker.ready watch so peers can discover it.
func TestReadmeIsServedAndAnnounced(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})

	evt := event.New(event.TypeWorkerReadme, "peer", nil)
	evt.RequestId = "r1"
	if !w.DispatchExtension(evt) {
		t.Fatal("hiw does not serve worker.readme")
	}
	if len(ch.sent) != 1 || ch.sent[0].Type != event.TypeRequestCompleted {
		t.Fatalf("expected one request.completed, got %+v", ch.sent)
	}

	w.publishReady()
	ready := ch.broadcasts[len(ch.broadcasts)-1]
	watch, _ := ready.Payload["watch"].([]map[string]any)
	found := false
	for _, e := range watch {
		if e["event"] == string(event.TypeWorkerReadme) {
			found = true
		}
	}
	if !found {
		t.Fatalf("worker.ready watch does not announce worker.readme: %+v", watch)
	}
}
