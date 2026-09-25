package eventbus

import (
	"testing"

	"github.com/niq-run/niq/core/event"
)

// TestCloneEventDeepCopiesPayload verifies that the engine's ingress clone owns
// its payload: mutating the original event's nested map/slice afterwards does
// not affect the clone. This is what stops a sender reusing its map from
// corrupting other recipients or the persisted copy (the json.Marshal race
// that panicked the process).
func TestCloneEventDeepCopiesPayload(t *testing.T) {
	orig := event.New("bash", "w", map[string]any{
		"command": "ls",
		"args":    []any{"-la", map[string]any{"k": "v"}},
		"tags":    []string{"a", "b"},
	})
	c := cloneEvent(orig)

	// Mutate the ORIGINAL deeply; the clone must stay intact.
	orig.Payload["command"] = "rm"                                     // top-level
	orig.Payload["args"].([]any)[1].(map[string]any)["k"] = "MUTATED"  // nested map
	orig.Payload["tags"] = []string{"zz"}                              // slice replace
	orig.Recipients = []string{"X"}

	if c.Payload["command"] != "ls" {
		t.Fatalf("clone payload top-level mutated: %v", c.Payload["command"])
	}
	if c.Payload["args"].([]any)[1].(map[string]any)["k"] != "v" {
		t.Fatalf("clone nested map mutated: %v", c.Payload)
	}
	if tags := c.Payload["tags"].([]string); len(tags) != 2 || tags[0] != "a" {
		t.Fatalf("clone slice mutated: %v", c.Payload["tags"])
	}
	if len(c.Recipients) != 0 {
		t.Fatalf("clone recipients aliased the original slice")
	}

	// Mutating the CLONE must not affect a copy of the original view either.
	c.Payload["command"] = "chmod"
	if orig.Payload["command"] != "rm" {
		t.Fatalf("clone write leaked back to original: %v", orig.Payload["command"])
	}
}