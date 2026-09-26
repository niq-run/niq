package eventbus

import (
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestIngressEventCanonicalizes verifies the engine's ingress normalizes the
// payload to the bus-wide JSON-generic shape (map[string]any / []any), so an
// in-process sender's concrete nested types ([]string) decode the same way an
// HTTP-crossing event would — the whole point of the "uniform map[string]any"
// contract. It also deep-copies: mutating the original afterwards does not
// affect the ingested copy (the json.Marshal race that panicked the process).
func TestIngressEventCanonicalizes(t *testing.T) {
	orig := event.New("bash", "w", map[string]any{
		"command": "ls",
		"args":    []any{"-la", map[string]any{"k": "v"}},
		"tags":    []string{"a", "b"}, // concrete slice → must become []any
	})
	orig.Recipients = []string{"R1"}

	ing, ok := ingressEvent(orig)
	if !ok {
		t.Fatal("ingressEvent dropped a JSON-serializable event")
	}

	// The concrete []string became the JSON-generic []any — the canonical shape.
	if _, isAny := ing.Payload["tags"].([]any); !isAny {
		t.Fatalf("tags did not canonicalize to []any: %T", ing.Payload["tags"])
	}

	// Mutate the ORIGINAL deeply; the ingested copy must stay intact.
	orig.Payload["command"] = "rm"
	orig.Payload["args"].([]any)[1].(map[string]any)["k"] = "MUTATED"
	orig.Payload["tags"] = []string{"zz"}
	orig.Recipients = []string{"X"}

	if ing.Payload["command"] != "ls" {
		t.Fatalf("ingested payload top-level mutated: %v", ing.Payload["command"])
	}
	if ing.Payload["args"].([]any)[1].(map[string]any)["k"] != "v" {
		t.Fatalf("ingested nested map mutated: %v", ing.Payload)
	}
	tags := ing.Payload["tags"].([]any)
	if len(tags) != 2 || tags[0] != "a" {
		t.Fatalf("ingested slice mutated: %v", ing.Payload["tags"])
	}
	if len(ing.Recipients) != 1 || ing.Recipients[0] != "R1" {
		t.Fatalf("ingested recipients aliased the original slice")
	}

	// Mutating the ingested copy must not leak back to the original.
	ing.Payload["command"] = "chmod"
	if orig.Payload["command"] != "rm" {
		t.Fatalf("ingest write leaked back to original: %v", orig.Payload["command"])
	}
}
