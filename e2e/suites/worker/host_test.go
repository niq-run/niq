//go:build e2e

package worker_e2e

import (
	"net/http"
	"testing"
)

// TestE2EHostLifecycleSuspendsResumesAndDestroys walks the lifecycle the host
// owns: a new in-process worker can be created, parked, woken, and destroyed,
// and after destruction the bus refuses to route to it.
func TestE2EHostLifecycleSuspendsResumesAndDestroys(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)

	code, body := g.PostJSON(t, "/p/"+project+"/api/workers/create", map[string]any{
		"id":        "t2",
		"type":      "timer",
		"placement": "in-process",
	})
	if code != http.StatusOK {
		t.Fatalf("create: %d %s", code, body)
	}

	if code, body := g.Get(t, "/p/"+project+"/api/workers"); code != http.StatusOK || !containsText(body, "t2") {
		t.Fatalf("the created worker is not listed: %d %s", code, body)
	}

	// Suspend parks it: the state is written, and the worker leaves the bus.
	if code, body := g.PostJSON(t, "/p/"+project+"/api/workers/t2/suspend", nil); code != http.StatusAccepted {
		t.Fatalf("suspend: %d %s", code, body)
	}
	if code, _ := g.PostJSON(t, "/p/"+project+"/api/workers/t2/event", map[string]any{"type": "list"}); code != http.StatusServiceUnavailable {
		t.Fatalf("a suspended worker still answered an event: %d", code)
	}

	// Resume brings it back.
	if code, body := g.PostJSON(t, "/p/"+project+"/api/workers/t2/resume", nil); code != http.StatusAccepted {
		t.Fatalf("resume: %d %s", code, body)
	}
	if code, body := g.PostJSON(t, "/p/"+project+"/api/workers/t2/event", map[string]any{"type": "list"}); code != http.StatusOK {
		t.Fatalf("the resumed worker does not answer: %d %s", code, body)
	}

	// Destroy is final for the identity: routing refuses it afterwards.
	if code, body := g.Delete(t, "/p/"+project+"/api/workers/t2"); code != http.StatusNoContent {
		t.Fatalf("destroy: %d %s", code, body)
	}
	if code, _ := g.PostJSON(t, "/p/"+project+"/api/workers/t2/event", map[string]any{"type": "list"}); code != http.StatusServiceUnavailable {
		t.Fatalf("a destroyed worker still answered an event: %d", code)
	}
}
