package webui

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/eventbus"
	eventbusapi "github.com/niq-run/niq/core/impl/eventbus/api"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/workers/hiw"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// lifecycleRig stands up the real thing the lifecycle endpoints talk to: a
// webui-hiw worker holding a bus channel, a host worker that answers restart,
// and an event log so the server's ask/reply correlation works.
type lifecycleRig struct {
	server   *Server
	addr     string
	hostReqs chan map[string]any
}

// fakeHost is a minimal host worker: it receives lifecycle events and answers
// them, recording the payloads it saw.
type fakeHost struct {
	baseworker.BaseWorker
	reqs chan map[string]any
}

func newLifecycleRig(t *testing.T) *lifecycleRig {
	t.Helper()
	dir := t.TempDir()
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(dir, "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, id := range []corebus.Identity{
		{WorkerID: "webui-hiw", Type: "hiw", PublishAllow: []event.PublishPattern{event.NewPublishPattern("*")}},
		{WorkerID: "host", Type: "host", PublishAllow: []event.PublishPattern{event.NewPublishPattern("request.*")}},
	} {
		if err := registry.Register(id); err != nil {
			t.Fatalf("register %s: %v", id.WorkerID, err)
		}
	}

	store := eventbus.NewMemoryEventStore()
	engine := eventbus.NewEngine(registry, store)
	log := eventbusapi.NewEventLog(engine, store)
	engine.OnEvent(log.Hook())

	listener := inprocess.NewInProcListener()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			ch, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			eventbus.Attach(ctx, engine, ch.WorkerID(), ch)
		}
	}()

	hiwCh := inprocess.NewWorkerSide("webui-hiw", listener)
	if err := hiwCh.Connect(ctx, "inproc://test"); err != nil {
		t.Fatalf("connect hiw: %v", err)
	}
	h := hiw.New(hiw.Config{ID: "webui-hiw", Bus: hiwCh})
	if err := h.Start(ctx); err != nil {
		t.Fatalf("start hiw: %v", err)
	}

	hostCh := inprocess.NewWorkerSide("host", listener)
	if err := hostCh.Connect(ctx, "inproc://test"); err != nil {
		t.Fatalf("connect host: %v", err)
	}
	host := &fakeHost{BaseWorker: baseworker.NewBaseWorker("host", hostCh), reqs: make(chan map[string]any, 8)}
	for _, typ := range []event.EventType{"resume", "suspend", "restart", "destroy", "update", "archive", "spawn"} {
		typ := typ
		host.Register(baseworker.Extension{Event: typ}, func(evt event.Event) {
			host.reqs <- evt.Payload
			host.ReplyCompleted(evt.WorkerId, evt.RequestId, map[string]any{
				"worker_id": evt.Payload["worker_id"],
				"status":    "ok",
				"placement": "in-process",
				"type":      "mcp",
			}, evt.TraceID)
		})
	}
	busCh, err := hostCh.Receive(ctx)
	if err != nil {
		t.Fatalf("host receive: %v", err)
	}
	go func() {
		for {
			select {
			case evt := <-busCh:
				host.DispatchExtension(evt)
			case <-ctx.Done():
				return
			}
		}
	}()

	s := New(h, log, engine, &stubCatalog{}, registry, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return &lifecycleRig{server: s, addr: addr, hostReqs: host.reqs}
}

// TestLifecycleEndpointsGoThroughHost verifies the WebUI has no privileged
// lifecycle path: restart, stop, start and delete are bus requests to the host
// worker, carrying the worker id.
func TestLifecycleEndpointsGoThroughHost(t *testing.T) {
	rig := newLifecycleRig(t)

	for _, tc := range []struct{ method, path, want string }{
		{"POST", "/api/workers/w1/restart", "restart"},
		{"POST", "/api/workers/w1/stop", "suspend"},
		{"POST", "/api/workers/w1/start", "resume"},
		{"DELETE", "/api/workers/w1", "destroy"},
	} {
		req, err := http.NewRequest(tc.method, "http://"+rig.addr+tc.path, nil)
		if err != nil {
			t.Fatalf("%s: new request: %v", tc.path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 202/204", tc.path, resp.StatusCode)
		}
		select {
		case payload := <-rig.hostReqs:
			if payload["worker_id"] != "w1" {
				t.Fatalf("%s reached the host with %v", tc.want, payload)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s never reached the host worker", tc.want)
		}
	}
}

// nopWorker is a ManagedWorker stub for lifecycle-free tests.
type nopWorker struct{ id string }

func (w *nopWorker) ID() string                  { return w.id }
func (w *nopWorker) Start(context.Context) error { return nil }
func (w *nopWorker) Stop() error                 { return nil }
func (w *nopWorker) Snapshot() ([]byte, error)   { return nil, nil }
func (w *nopWorker) Restore([]byte) error        { return nil }

// nopChannel is a WorkerSideChannel stub; these tests never route events.
type nopChannel struct{}

func (nopChannel) ID() string                                          { return "nop" }
func (nopChannel) Send(context.Context, event.Event, ...string) error  { return nil }
func (nopChannel) Broadcast(context.Context, event.Event) error        { return nil }
func (nopChannel) Receive(context.Context) (<-chan event.Event, error) { return nil, nil }
func (nopChannel) Close() error                                        { return nil }

func mustPut(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("PUT", url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

// expectHostCall drains one recorded host payload and checks the event type it
// arrived under. Each helper below is one endpoint, so the payload shape is the
// contract the host tool sees.
func expectHostCall(t *testing.T, rig *lifecycleRig, wantID string) map[string]any {
	t.Helper()
	select {
	case payload := <-rig.hostReqs:
		if payload["worker_id"] != wantID {
			t.Fatalf("host payload = %v, want worker_id %s", payload, wantID)
		}
		return payload
	case <-time.After(3 * time.Second):
		t.Fatalf("no call reached the host worker")
		return nil
	}
}

// TestUpdateWorkerParamsGoesThroughHost verifies the params editor is a bus call
// to the host's `update` tool, carrying the params object as posted.
func TestUpdateWorkerParamsGoesThroughHost(t *testing.T) {
	rig := newLifecycleRig(t)
	resp, err := http.DefaultClient.Do(mustPut(t, "http://"+rig.addr+"/api/workers/w1/params", `{"params":{"fixed":true}}`))
	if err != nil {
		t.Fatalf("PUT params: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	payload := expectHostCall(t, rig, "w1")
	params, ok := payload["params"].(map[string]any)
	if !ok || params["fixed"] != true {
		t.Fatalf("params payload = %v", payload["params"])
	}
	// A missing params field clears them: the host must see an empty object,
	// not an absent key.
	resp, err = http.DefaultClient.Do(mustPut(t, "http://"+rig.addr+"/api/workers/w1/params", `{}`))
	if err != nil {
		t.Fatalf("PUT clear: %v", err)
	}
	resp.Body.Close()
	payload = expectHostCall(t, rig, "w1")
	if m, ok := payload["params"].(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("cleared params payload = %v, want an empty object", payload["params"])
	}
}

// TestUpdateWorkerMetaGoesThroughHost verifies the metadata editor is the same
// `update` tool call, and that an empty tag list is sent as a clear ([] not nil).
func TestUpdateWorkerMetaGoesThroughHost(t *testing.T) {
	rig := newLifecycleRig(t)
	resp, err := http.DefaultClient.Do(mustPut(t, "http://"+rig.addr+"/api/workers/w1/meta",
		`{"label":"值班","tags":["ops"],"description":"nightly"}`))
	if err != nil {
		t.Fatalf("PUT meta: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	payload := expectHostCall(t, rig, "w1")
	if payload["label"] != "值班" || payload["description"] != "nightly" {
		t.Fatalf("meta payload = %v", payload)
	}
	if tags := argStrings(payload["tags"]); len(tags) != 1 || tags[0] != "ops" {
		t.Fatalf("tags payload = %v", payload["tags"])
	}

	resp, err = http.DefaultClient.Do(mustPut(t, "http://"+rig.addr+"/api/workers/w1/meta", `{"label":"","tags":[]}`))
	if err != nil {
		t.Fatalf("PUT meta clear: %v", err)
	}
	resp.Body.Close()
	payload = expectHostCall(t, rig, "w1")
	if tags := argStrings(payload["tags"]); tags == nil || len(tags) != 0 {
		t.Fatalf("cleared tags payload = %v, want an empty list", payload["tags"])
	}
}

// TestArchiveGoesThroughHost verifies archive/unarchive is ONE host call: the
// WebUI no longer suspends, moves files and revokes identities itself.
func TestArchiveGoesThroughHost(t *testing.T) {
	rig := newLifecycleRig(t)
	for _, archived := range []bool{true, false} {
		body := `{"archived":false}`
		if archived {
			body = `{"archived":true}`
		}
		resp, err := http.DefaultClient.Do(mustPost(t, "http://"+rig.addr+"/api/workers/w1/archived", body))
		if err != nil {
			t.Fatalf("PUT archive: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		payload := expectHostCall(t, rig, "w1")
		if got, _ := payload["archived"].(bool); got != archived {
			t.Fatalf("archived payload = %v, want %v", payload["archived"], archived)
		}
	}
}

// argStrings reads a string list that may have been re-encoded by the bus.
func argStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, _ := item.(string)
			out = append(out, s)
		}
		return out
	}
	return nil
}

func mustPost(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}
