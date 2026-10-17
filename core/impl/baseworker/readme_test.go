package baseworker

import (
	"context"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestReadmeCustomization verifies the declaration-level readme keys: readme
// replaces the built-in guide, readme_append appends to it, and both render
// {{key}} placeholders from params.
func TestReadmeCustomization(t *testing.T) {
	builtin := func() string { return "builtin guide" }

	if got := readmeText(builtin, nil); got != "builtin guide" {
		t.Fatalf("no params = %q, want the built-in guide", got)
	}
	if got := readmeText(builtin, map[string]any{"readme": "custom"}); got != "custom" {
		t.Fatalf("override = %q, want custom", got)
	}
	if got := readmeText(builtin, map[string]any{"readme_append": "extra"}); got != "builtin guide\nextra" {
		t.Fatalf("append = %q, want builtin + extra", got)
	}

	params := map[string]any{
		"readme": "You are the {{role}} worker.\nTools: {{tools}}\nMissing: {{nope}}",
		"role":   "web",
		"tools":  []any{"search", "fetch_content"},
	}
	got := readmeText(builtin, params)
	want := "You are the web worker.\nTools: search, fetch_content\nMissing: {{nope}}"
	if got != want {
		t.Fatalf("rendered = %q, want %q", got, want)
	}
}

// TestRegisterReadmeReplies verifies the capability is registered and answers a
// directed worker.readme call with the customized text.
func TestRegisterReadmeReplies(t *testing.T) {
	ch := &recordingChannel{}
	w := NewBaseWorker("w", ch)
	w.RegisterReadme("guide", func() string { return "builtin" }, map[string]any{"readme": "hello {{name}}", "name": "mcp"})

	evt := event.New(event.TypeWorkerReadme, "caller", nil)
	evt.RequestId = "req-1"
	if !w.DispatchExtension(evt) {
		t.Fatal("worker.readme is not registered")
	}
	if len(ch.sent) != 1 {
		t.Fatalf("sent %d replies, want 1", len(ch.sent))
	}
	if got, _ := ch.sent[0].Payload["result"].(string); got != "hello mcp" {
		t.Fatalf("reply = %q, want the rendered override", got)
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

// TestRenderParamsKeepsPlainBraces verifies only doubled braces are treated as
// placeholders, so JSON examples in a guide survive untouched.
func TestRenderParamsKeepsPlainBraces(t *testing.T) {
	in := `call mcp.tool.call { "server": "x" } and {{known}}`
	got := renderParams(in, map[string]any{"known": "yes"})
	if got != `call mcp.tool.call { "server": "x" } and yes` {
		t.Fatalf("rendered = %q", got)
	}
	if got := renderParams("{{unclosed", nil); got != "{{unclosed" {
		t.Fatalf("unclosed placeholder = %q, want it untouched", got)
	}
}
