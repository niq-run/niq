package project

import "testing"

// TestUpdateWorkerParams verifies a params edit replaces the declaration's
// params verbatim (an empty object clears them) and that unknown ids are
// rejected.
func TestUpdateWorkerParams(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := CreateProject("proj", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := AppendWorkerDecl("proj", WorkerConfig{
		Type:   "mcp",
		ID:     "m",
		Params: map[string]any{"expose": "agentic"},
	}); err != nil {
		t.Fatalf("AppendWorkerDecl: %v", err)
	}

	if err := UpdateWorkerParams("proj", "m", map[string]any{"fixed": true, "expose": "ready"}); err != nil {
		t.Fatalf("UpdateWorkerParams: %v", err)
	}
	p, err := LoadProject("proj")
	if err != nil {
		t.Fatal(err)
	}
	wc, ok := FindWorker(p, "m")
	if !ok {
		t.Fatal("worker declaration disappeared")
	}
	if wc.Params["fixed"] != true || wc.Params["expose"] != "ready" {
		t.Fatalf("params = %+v, want the replaced set", wc.Params)
	}

	// An empty edit clears the params instead of persisting an empty map.
	if err := UpdateWorkerParams("proj", "m", map[string]any{}); err != nil {
		t.Fatalf("UpdateWorkerParams(clear): %v", err)
	}
	p, _ = LoadProject("proj")
	if wc, _ := FindWorker(p, "m"); wc.Params != nil {
		t.Fatalf("params = %+v, want nil after clear", wc.Params)
	}

	if err := UpdateWorkerParams("proj", "missing", map[string]any{"a": 1}); err == nil {
		t.Fatal("UpdateWorkerParams on an undeclared worker should fail")
	}
}
