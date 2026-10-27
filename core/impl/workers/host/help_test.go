package host

import (
	"context"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// ask dispatches one worker.help request to the host and returns the text.
func ask(t *testing.T, w *HostWorker, ch *recordingChannel, args map[string]any) string {
	t.Helper()
	before := len(ch.sent)
	evt := event.New(event.TypeWorkerHelp, "reason", args)
	evt.RequestId = "req-help"
	if !w.DispatchExtension(evt) {
		t.Fatal("host does not serve worker.help")
	}
	if len(ch.sent) != before+1 || ch.sent[len(ch.sent)-1].Type != event.TypeRequestCompleted {
		t.Fatalf("expected one request.completed, got %+v", ch.sent[before:])
	}
	text, _ := ch.sent[len(ch.sent)-1].Payload["result"].(string)
	return text
}

// TestWorkerHelpSpawnReason verifies the request from the design: asking for
// help on spawn with params.worker=reason returns the reason spawn guide.
func TestWorkerHelpSpawnReason(t *testing.T) {
	ch := &recordingChannel{}
	w := New(Config{ID: "host", Bus: ch})

	got := ask(t, w, ch, map[string]any{
		"event_name": "spawn",
		"params":     map[string]any{"worker": "reason"},
	})
	for _, want := range []string{
		"# Spawn a reason worker",
		`"type": "reason"`,
		"`id`",
		"`goal`",
		"`brief`",
		"`programs`",
		"`subscriptions`",
		"`provider`",
		"`budget_soft`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("reason guide missing %q:\n%s", want, got)
		}
	}
}

// TestWorkerHelpSpawnGeneral verifies the general spawn reference is returned
// when no type selector is given, and that an undocumented type still gets it
// with an explicit note.
func TestWorkerHelpSpawnGeneral(t *testing.T) {
	ch := &recordingChannel{}
	w := New(Config{ID: "host", Bus: ch})

	general := ask(t, w, ch, map[string]any{"event_name": "spawn"})
	for _, want := range []string{"# spawn", "Type-specific guides", "workspace"} {
		if !strings.Contains(general, want) {
			t.Fatalf("general guide missing %q:\n%s", want, general)
		}
	}

	other := ask(t, w, ch, map[string]any{
		"event_name": "spawn",
		"params":     map[string]any{"worker": "timer"},
	})
	if !strings.Contains(other, "No type-specific guide exists") {
		t.Fatalf("undocumented type must be called out:\n%s", other)
	}
}

// TestWorkerHelpIndexAndGenericTools verifies the no-argument index and that
// an event without a custom guide (suspend) renders its declared contract.
func TestWorkerHelpIndexAndGenericTools(t *testing.T) {
	ch := &recordingChannel{}
	w := New(Config{ID: "host", Bus: ch})

	index := ask(t, w, ch, map[string]any{})
	for _, want := range []string{"`spawn`", "`suspend`", "`resume`", "`worker.help`"} {
		if !strings.Contains(index, want) {
			t.Fatalf("index missing %q:\n%s", want, index)
		}
	}

	guide := ask(t, w, ch, map[string]any{"event_name": "suspend"})
	for _, want := range []string{"# suspend", "Suspend a running worker", `"worker_id"`} {
		if !strings.Contains(guide, want) {
			t.Fatalf("generic suspend guide missing %q:\n%s", want, guide)
		}
	}
}

// recordingChannel captures sends so a test can inspect replies.
type recordingChannel struct {
	sent []event.Event
}

func (c *recordingChannel) ID() string { return "rec" }
func (c *recordingChannel) Send(_ context.Context, evt event.Event, _ ...string) error {
	c.sent = append(c.sent, evt)
	return nil
}
func (c *recordingChannel) Broadcast(context.Context, event.Event) error        { return nil }
func (c *recordingChannel) Receive(context.Context) (<-chan event.Event, error) { return nil, nil }
func (c *recordingChannel) Close() error                                        { return nil }
