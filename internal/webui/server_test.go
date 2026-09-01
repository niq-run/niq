package webui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/eventbus"
)

// New only dereferences its dependencies inside route handlers, so a bare
// server with nil deps is enough to exercise bind/serve. Established here so
// we test dynamic-port allocation without standing up a full HIW/engine.
func TestBindDynamicWebUIPort(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if addr == ":0" || addr == "" {
		t.Fatalf("resolved addr = %q, want a real host:port", addr)
	}
	if got := s.ResolvedAddr(); got != addr {
		t.Fatalf("ResolvedAddr = %q, want %q", got, addr)
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	conn.Close()

	again, err := s.Bind()
	if err != nil || again != addr {
		t.Fatalf("second Bind = %q (%v), want %q", again, err, addr)
	}
}

// TestHandleUpdateAllow verifies PUT /api/workers/{id}/allow edits the bus
// registry: subscribe_allow is replaced wholesale (source restriction kept),
// a missing publish_allow keeps the current value, and unknown workers 404.
func TestHandleUpdateAllow(t *testing.T) {
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := registry.Register(corebus.Identity{
		WorkerID:       "w1",
		Type:           "timer",
		PublishAllow:   []string{"*"},
		SubscribeAllow: []event.EventPattern{{Type: "worker.discover"}},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	s := New(nil, nil, nil, nil, registry, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	put := func(id, body string) (*http.Response, error) {
		req, err := http.NewRequest("PUT", "http://"+addr+"/api/workers/"+id+"/allow", strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}

	// Source-restricted subscription replaces the list; publish_allow untouched.
	resp, err := put("w1", `{"subscribe_allow":[{"type":"request.completed","source_id":"timer"},{"type":"worker.ready"}]}`)
	if err != nil {
		t.Fatalf("PUT allow: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT allow status = %d, want 200", resp.StatusCode)
	}

	idt, ok := registry.Lookup("w1")
	if !ok {
		t.Fatal("identity gone after update")
	}
	if len(idt.SubscribeAllow) != 2 ||
		idt.SubscribeAllow[0].Type != "request.completed" || idt.SubscribeAllow[0].SourceID != "timer" ||
		idt.SubscribeAllow[1].Type != "worker.ready" || idt.SubscribeAllow[1].SourceID != "" {
		t.Fatalf("subscribe_allow = %+v, want [request.completed@timer, worker.ready]", idt.SubscribeAllow)
	}
	if len(idt.PublishAllow) != 1 || idt.PublishAllow[0] != "*" {
		t.Fatalf("publish_allow clobbered = %+v, want [*]", idt.PublishAllow)
	}

	// Empty subscribe_allow clears the list (worker receives no broadcasts).
	resp, err = put("w1", `{"subscribe_allow":[]}`)
	if err != nil {
		t.Fatalf("PUT clear: %v", err)
	}
	resp.Body.Close()
	idt, _ = registry.Lookup("w1")
	if len(idt.SubscribeAllow) != 0 {
		t.Fatalf("clear failed: subscribe_allow = %+v", idt.SubscribeAllow)
	}

	// Unknown worker → 404.
	resp, err = put("nope", `{"subscribe_allow":[]}`)
	if err != nil {
		t.Fatalf("PUT unknown: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown worker status = %d, want 404", resp.StatusCode)
	}
}

// TestWebUIContextProjectMode verifies a project instance reports itself as a
// project (not control) via /api via /api/context.
func TestWebUIContextProjectMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(nil, nil, nil, nil, nil, ":0", false)
	s.SetContext(ContextInfo{Mode: "project", Project: "mydemo", ControlURL: "http://127.0.0.1:9527"})
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	resp, err := http.Get("http://" + addr + "/api/context")
	if err != nil {
		t.Fatalf("GET context: %v", err)
	}
	defer resp.Body.Close()
	var ci ContextInfo
	if err := json.NewDecoder(resp.Body).Decode(&ci); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ci.Mode != "project" || ci.Project != "mydemo" {
		t.Fatalf("context = %+v, want project/mydemo", ci)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned %v", err)
	}
}

func TestWebUIServeOnBoundListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(nil, nil, nil, nil, nil, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	conn.Close()

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned %v", err)
	}
}
