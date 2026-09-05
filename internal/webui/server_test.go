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
	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/eventbus"
	"github.com/niq-run/niq/pkg/services/workerhost"
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
		PublishAllow:   []event.PublishPattern{event.NewPublishPattern("*")},
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
	if len(idt.PublishAllow) != 1 || idt.PublishAllow[0].Type != "*" {
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

// stubUnmanagedController drives handleWorkers's declaration merge with
// in-memory statuses.
type stubUnmanagedController struct {
	declared []UnmanagedStatus
}

func (c *stubUnmanagedController) Start(id string) error         { return nil }
func (c *stubUnmanagedController) Stop(id string) error          { return nil }
func (c *stubUnmanagedController) Restart(id string) error       { return nil }
func (c *stubUnmanagedController) List() []UnmanagedStatus       { return nil }
func (c *stubUnmanagedController) Declared() []UnmanagedStatus   { return c.declared }
func (c *stubUnmanagedController) Remove(id string) error        { return nil }

// fakeBuilderSpec returns a SpawnSpec whose closures are never called by the
// code paths under test (RestoreSuspended builds the spec without connecting).
func fakeBuilderSpec(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	return worker.SpawnSpec{Config: cfg}, nil
}

// TestHandleWorkersDeclaredMerge verifies how project.json declarations merge
// into GET /api/workers: never-started externals carry the unmanaged badge,
// managed declarations known to the worker service do not, and managed
// declarations absent from both the registry and the worker service surface as
// managed rows in the "stopped" state (start spawns them via the host worker).
func TestHandleWorkersDeclaredMerge(t *testing.T) {
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, id := range []string{"live-ext", "susp-managed"} {
		if err := registry.Register(corebus.Identity{WorkerID: id, Type: "mcp"}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	workerSvc := workerhost.New()
	workerSvc.RegisterBuilder("reason", fakeBuilderSpec)
	if err := workerSvc.RestoreSuspended(worker.WorkerConfig{ID: "susp-managed", Type: "reason"}, nil); err != nil {
		t.Fatalf("restore suspended: %v", err)
	}

	engine := eventbus.NewEngine(registry, eventbus.NewMemoryEventStore())
	s := New(nil, nil, engine, workerSvc, registry, ":0", false)
	s.SetUnmanagedController(&stubUnmanagedController{declared: []UnmanagedStatus{
		{ID: "live-ext", Type: "mcp", State: "running", Alive: true},
		{ID: "idle-ext", Type: "mcp", State: "stopped"},
		{ID: "susp-managed", Type: "reason", Managed: true, State: "stopped"},
		{ID: "ghost-managed", Type: "timer", Managed: true, State: "stopped"},
	}})
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := http.Get("http://" + addr + "/api/workers")
	if err != nil {
		t.Fatalf("GET workers: %v", err)
	}
	defer resp.Body.Close()
	var views []WorkerView
	if err := json.NewDecoder(resp.Body).Decode(&views); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]WorkerView{}
	for _, v := range views {
		byID[v.ID] = v
	}

	if v := byID["live-ext"]; !v.Unmanaged || v.UnmanagedState != "running" || v.Managed {
		t.Fatalf("live-ext = %+v, want unmanaged/running", v)
	}
	if v := byID["idle-ext"]; !v.Unmanaged || v.UnmanagedState != "stopped" || v.Managed {
		t.Fatalf("idle-ext = %+v, want unmanaged/stopped", v)
	}
	// The suspended managed worker is known to the worker service: its live
	// state wins and the managed declaration must not badge it unmanaged.
	if v := byID["susp-managed"]; !v.Managed || v.State != "suspended" || v.Unmanaged {
		t.Fatalf("susp-managed = %+v, want managed/suspended without unmanaged badge", v)
	}
	// Declared but instantiated nowhere: surfaced for the start button.
	if v := byID["ghost-managed"]; !v.Managed || v.State != "stopped" || v.Unmanaged {
		t.Fatalf("ghost-managed = %+v, want managed/stopped", v)
	}
}

// TestHandleUnmanagedStartConflict verifies start refuses a worker the worker
// service already owns (suspend/resume own that lifecycle), and that worker
// creation is 404 without an assembly-provided creator.
func TestHandleUnmanagedStartConflict(t *testing.T) {
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	workerSvc := workerhost.New()
	workerSvc.RegisterBuilder("reason", fakeBuilderSpec)
	if err := workerSvc.RestoreSuspended(worker.WorkerConfig{ID: "susp-managed", Type: "reason"}, nil); err != nil {
		t.Fatalf("restore suspended: %v", err)
	}

	engine := eventbus.NewEngine(registry, eventbus.NewMemoryEventStore())
	s := New(nil, nil, engine, workerSvc, registry, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := http.Post("http://"+addr+"/api/workers/susp-managed/start", "", nil)
	if err != nil {
		t.Fatalf("POST start: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start status = %d, want 409", resp.StatusCode)
	}

	resp, err = http.Post("http://"+addr+"/api/workers/create", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST create: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("create status = %d, want 404 without a creator", resp.StatusCode)
	}
}
