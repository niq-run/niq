package baseworker

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// helpReply dispatches one worker.help request and returns the reply text.
func helpReply(t *testing.T, w *BaseWorker, ch *recordingChannel, args map[string]any) (string, event.EventType) {
	t.Helper()
	before := len(ch.sent)
	evt := event.New(event.TypeWorkerHelp, "peer", args)
	evt.RequestId = "req-help"
	if !w.DispatchExtension(evt) {
		t.Fatal("worker.help is not registered")
	}
	if len(ch.sent) != before+1 {
		t.Fatalf("sent %d replies, want 1", len(ch.sent)-before)
	}
	reply := ch.sent[len(ch.sent)-1]
	text, _ := reply.Payload["result"].(string)
	return text, reply.Type
}

// TestHelpIndexAndGenericGuide verifies worker.help without a custom guide:
// no event_name lists every served event, and a named event renders its
// declared description, argument schema and reply schema.
func TestHelpIndexAndGenericGuide(t *testing.T) {
	ch := &recordingChannel{}
	w := NewBaseWorker("w", ch)
	w.Register(Extension{
		Event:       "spawn",
		Description: "Create a new worker.",
		Parameters:  ObjectSchema(map[string]any{"type": map[string]any{"type": "string"}}),
		Response:    StringSchema("the created worker"),
	}, func(event.Event) {})
	w.RegisterHelp(nil)

	index, typ := helpReply(t, &w, ch, map[string]any{})
	if typ != event.TypeRequestCompleted {
		t.Fatalf("index reply = %s, want request.completed", typ)
	}
	for _, want := range []string{"# w — help", "`spawn`", "Create a new worker.", "`worker.help`"} {
		if !strings.Contains(index, want) {
			t.Fatalf("index missing %q:\n%s", want, index)
		}
	}

	guide, typ := helpReply(t, &w, ch, map[string]any{"event_name": "spawn"})
	if typ != event.TypeRequestCompleted {
		t.Fatalf("guide reply = %s, want request.completed", typ)
	}
	for _, want := range []string{"# spawn", "Create a new worker.", "## Arguments", `"type": "string"`, "## Reply"} {
		if !strings.Contains(guide, want) {
			t.Fatalf("generic guide missing %q:\n%s", want, guide)
		}
	}
}

// TestHelpCustomGuideSelectsByParams verifies a HelpFunc wins when it
// recognizes params, and falls back to the generic guide when it does not.
func TestHelpCustomGuideSelectsByParams(t *testing.T) {
	ch := &recordingChannel{}
	w := NewBaseWorker("w", ch)
	w.Register(Extension{Event: "spawn", Description: "Create a new worker."}, func(event.Event) {})
	w.RegisterHelp(map[event.EventType]HelpFunc{
		"spawn": func(params map[string]any) (string, bool) {
			if ArgString(params, "worker") == "reason" {
				return "# Spawn a reason worker", true
			}
			return "", false
		},
	})

	got, _ := helpReply(t, &w, ch, map[string]any{
		"event_name": "spawn",
		"params":     map[string]any{"worker": "reason"},
	})
	if got != "# Spawn a reason worker" {
		t.Fatalf("custom guide = %q", got)
	}

	got, _ = helpReply(t, &w, ch, map[string]any{
		"event_name": "spawn",
		"params":     map[string]any{"worker": "timer"},
	})
	if !strings.Contains(got, "# spawn") || !strings.Contains(got, "No event-specific guide matched") {
		t.Fatalf("unmatched params must fall back to the generic guide:\n%s", got)
	}
}

// TestHelpUnknownEventFails verifies an unknown event_name is answered with
// request.failed naming the events the worker actually serves.
func TestHelpUnknownEventFails(t *testing.T) {
	ch := &recordingChannel{}
	w := NewBaseWorker("w", ch)
	w.Register(Extension{Event: "spawn"}, func(event.Event) {})
	w.RegisterHelp(nil)

	evt := event.New(event.TypeWorkerHelp, "peer", map[string]any{"event_name": "nope"})
	evt.RequestId = "req-1"
	w.DispatchExtension(evt)

	if len(ch.sent) != 1 || ch.sent[0].Type != event.TypeRequestFailed {
		t.Fatalf("expected request.failed, got %+v", ch.sent)
	}
	msg, _ := ch.sent[0].Payload["error"].(string)
	if !strings.Contains(msg, `does not serve event "nope"`) || !strings.Contains(msg, "spawn") {
		t.Fatalf("error = %q, want the unknown event and the served list", msg)
	}
}

// TestHelpHidesInternalFromPeers verifies Internal extensions are listed for
// their owner only, matching the dispatch scope rule.
func TestHelpHidesInternalFromPeers(t *testing.T) {
	ch := &recordingChannel{}
	w := NewBaseWorker("w", ch)
	w.Register(Extension{Event: "my.op", Internal: true}, func(event.Event) {})
	w.RegisterHelp(nil)

	peerIndex, _ := helpReply(t, &w, ch, map[string]any{})
	if strings.Contains(peerIndex, "my.op") {
		t.Fatalf("peer index leaked an internal capability:\n%s", peerIndex)
	}

	// The owner sees it in its own index.
	before := len(ch.sent)
	evt := event.New(event.TypeWorkerHelp, "w", map[string]any{})
	evt.RequestId = "req-self"
	if !w.DispatchExtension(evt) {
		t.Fatal("worker.help is not registered")
	}
	if len(ch.sent) != before+1 {
		t.Fatalf("no self reply")
	}
	selfIndex, _ := ch.sent[len(ch.sent)-1].Payload["result"].(string)
	if !strings.Contains(selfIndex, "my.op") {
		t.Fatalf("owner index must include internal capabilities:\n%s", selfIndex)
	}
}
