package baseworker

import (
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestReplyCompletedShapes pins the helper's convenience contract: an object
// reply IS the payload (no wrapper, so the event log shows the reply), while a
// text/number/array reply is wrapped under "result" (a payload cannot be a bare
// non-object). Workers may still build a request.completed payload by hand.
func TestReplyCompletedShapes(t *testing.T) {
	cases := []struct {
		name  string
		reply any
		check func(t *testing.T, payload map[string]any)
	}{
		{"object", map[string]any{"mounts": []any{"/a"}, "primary": "/a"}, func(t *testing.T, p map[string]any) {
			if _, wrapped := p["result"]; wrapped {
				t.Fatalf("object reply must not be wrapped: %+v", p)
			}
			if p["primary"] != "/a" {
				t.Fatalf("payload = %+v, want the object itself", p)
			}
		}},
		{"text", "Exit code: 0", func(t *testing.T, p map[string]any) {
			if p["result"] != "Exit code: 0" {
				t.Fatalf("payload = %+v, want {result: text}", p)
			}
		}},
		{"array", []any{"a", "b"}, func(t *testing.T, p map[string]any) {
			if _, ok := p["result"].([]any); !ok {
				t.Fatalf("payload = %+v, want the array under result", p)
			}
		}},
		{"number", 42, func(t *testing.T, p map[string]any) {
			if p["result"] != 42 {
				t.Fatalf("payload = %+v, want the number under result", p)
			}
		}},
		{"content blocks", map[string]any{"content": []any{map[string]any{"type": "image"}}}, func(t *testing.T, p map[string]any) {
			if _, ok := p["content"]; !ok {
				t.Fatalf("payload = %+v, want the content payload itself", p)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := &recordingChannel{}
			w := NewBaseWorker("w1", ch)
			w.ReplyCompleted("caller", "call-1", c.reply, "trace")
			if len(ch.sent) != 1 {
				t.Fatalf("sent = %d events, want 1", len(ch.sent))
			}
			evt := ch.sent[0]
			if evt.Type != event.TypeRequestCompleted || evt.RequestId != "call-1" || evt.TraceID != "trace" {
				t.Fatalf("envelope = %+v", evt)
			}
			c.check(t, evt.Payload)
		})
	}
}
