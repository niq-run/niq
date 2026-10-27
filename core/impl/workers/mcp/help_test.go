package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

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

// helpText dispatches one worker.help request and returns the reply text.
func helpText(t *testing.T, w *Worker, ch *recordingChannel, args map[string]any) string {
	t.Helper()
	before := len(ch.sent)
	evt := event.New(event.TypeWorkerHelp, "caller", args)
	evt.RequestId = "req-help"
	if !w.DispatchExtension(evt) {
		t.Fatal("mcp does not serve worker.help")
	}
	if len(ch.sent) != before+1 || ch.sent[len(ch.sent)-1].Type != event.TypeRequestCompleted {
		t.Fatalf("expected one request.completed, got %+v", ch.sent[before:])
	}
	text, _ := ch.sent[len(ch.sent)-1].Payload["result"].(string)
	return text
}

// TestHelpCoversEveryFixedEvent verifies each server/tool management event has
// a usage guide (config-only workers still register the same guides; only the
// events themselves are absent).
func TestHelpCoversEveryFixedEvent(t *testing.T) {
	ch := &recordingChannel{}
	w := New(WorkerConfig{ID: "mcp", Bus: ch, Expose: ExposeAgentic})

	cases := []struct {
		event event.EventType
		want  []string
	}{
		{EvtServerList, []string{"# mcp.server.list", "cached tool count", "mcp.tool.list"}},
		{EvtServerAdd, []string{"# mcp.server.add", "name (required) and url (required)", "privileged"}},
		{EvtServerRemove, []string{"# mcp.server.remove", "withdraw its tools"}},
		{EvtServerUpdate, []string{"# mcp.server.update", "headers replace"}},
		{EvtToolList, []string{"# mcp.tool.list", "## Workflow", "mcp.tool.call"}},
		{EvtToolCall, []string{"# mcp.tool.call", "input schema", "reconnects on the next call"}},
		{EvtExpose, []string{"# mcp.expose", "## Modes", "agentic"}},
	}
	for _, tc := range cases {
		text := helpText(t, w, ch, map[string]any{"event_name": string(tc.event)})
		for _, want := range tc.want {
			if !strings.Contains(text, want) {
				t.Fatalf("%s guide missing %q:\n%s", tc.event, want, text)
			}
		}
	}
}

// TestHelpIsServerAgnostic verifies params never changes an mcp guide: usage is
// the same whatever server or tool the caller has in mind.
func TestHelpIsServerAgnostic(t *testing.T) {
	ch := &recordingChannel{}
	w := New(WorkerConfig{ID: "mcp", Bus: ch, Expose: ExposeAgentic})

	for _, evt := range []event.EventType{EvtToolList, EvtToolCall} {
		plain := helpText(t, w, ch, map[string]any{"event_name": string(evt)})
		selected := helpText(t, w, ch, map[string]any{
			"event_name": string(evt),
			"params":     map[string]any{"server": "github"},
		})
		if selected != plain {
			t.Fatalf("%s guide changed with params:\n--- plain ---\n%s\n--- selected ---\n%s",
				evt, plain, selected)
		}
	}
}

// TestHelpIndexAndGenericTool verifies the index lists the mcp surface, and a
// ready-mode tool (no static guide) renders its remote contract generically.
func TestHelpIndexAndGenericTool(t *testing.T) {
	ch := &recordingChannel{}
	w := New(WorkerConfig{ID: "mcp", Bus: ch, Fixed: true})

	index := helpText(t, w, ch, map[string]any{})
	for _, want := range []string{
		"`mcp.server.list`", "`mcp.tool.list`", "`mcp.tool.call`",
		"`worker.help`", "`worker.readme`",
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("index missing %q:\n%s", want, index)
		}
	}

	// A flattened ready-mode remote tool: no static guide, contract only.
	w.Register(baseworker.Extension{
		Event:       "mcp.srv.echo",
		Description: "Echo a message.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"text": map[string]any{"type": "string"},
		}},
		Response: map[string]any{"type": "string"},
	}, func(event.Event) {})

	guide := helpText(t, w, ch, map[string]any{"event_name": "mcp.srv.echo"})
	for _, want := range []string{"# mcp.srv.echo", "Echo a message.", "## Arguments", `"text"`, `"type": "string"`} {
		if !strings.Contains(guide, want) {
			t.Fatalf("generic tool guide missing %q:\n%s", want, guide)
		}
	}
}

// TestMetaCapabilitiesAreReserved verifies a remote ready-mode tool can never
// overwrite the worker's own meta capabilities.
func TestMetaCapabilitiesAreReserved(t *testing.T) {
	for _, evt := range []event.EventType{event.TypeWorkerReadme, event.TypeWorkerHelp} {
		if !reservedEvent(evt) {
			t.Fatalf("%s is not reserved", evt)
		}
	}
}
