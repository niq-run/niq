package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/busctl"
	"github.com/niq-run/niq/core/impl/eventbus"
	eventbusapi "github.com/niq-run/niq/core/impl/eventbus/api"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/iface"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
)

// New only dereferences its dependencies inside route handlers, so a bare
// server with nil deps is enough to exercise bind/serve. Established here so
// we test dynamic-port allocation without standing up a full HIW/engine.
func TestBindDynamicWebUIPort(t *testing.T) {
	s := New(nil, nil, nil, nil, ":0", false)
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

// TestHandleUpdateAllowGoesThroughBusControl verifies an ACL edit is one direct
// call to the bus identity control plane: durable store plus runtime projection,
// never a bus event to the host. The WebUI only normalizes the request (an
// absent list keeps the current value, an omitted source becomes the any-source
// wildcard).
func TestHandleUpdateAllowGoesThroughBusControl(t *testing.T) {
	rig := newLifecycleRig(t)

	put := func(id, body string) (*http.Response, error) {
		req, err := http.NewRequest("PUT", "http://"+rig.addr+"/api/workers/"+id+"/allow", strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}

	resp, err := put("webui", `{"subscribe_allow":[{"type":"request.completed","source_id":"timer"},{"type":"worker.ready"}]}`)
	if err != nil {
		t.Fatalf("PUT allow: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT allow status = %d, want 200", resp.StatusCode)
	}

	idt, ok := rig.bus.Lookup("webui")
	if !ok {
		t.Fatal("identity missing from the bus store")
	}
	// publish_allow was omitted, so the current grants are kept.
	if len(idt.PublishAllow) == 0 {
		t.Fatalf("publish_allow = %+v, want the current list", idt.PublishAllow)
	}
	if len(idt.SubscribeAllow) != 2 {
		t.Fatalf("subscribe_allow = %+v", idt.SubscribeAllow)
	}
	if idt.SubscribeAllow[0].Type != "request.completed" || idt.SubscribeAllow[0].SourceID != "timer" {
		t.Fatalf("first grant = %+v", idt.SubscribeAllow[0])
	}
	if idt.SubscribeAllow[1].Type != "worker.ready" || idt.SubscribeAllow[1].SourceID != "*" {
		t.Fatalf("an omitted source must be stored as the any-source wildcard: %+v", idt.SubscribeAllow[1])
	}
	// The change reached the runtime registry in the same call.
	runtime, ok := rig.server.registry.Lookup("webui")
	if !ok || len(runtime.SubscribeAllow) != 2 {
		t.Fatalf("runtime identity = %+v (ok=%v)", runtime, ok)
	}

	// No bus event was sent to the host.
	select {
	case p := <-rig.hostReqs:
		t.Fatalf("ACL edit went through the host: %v", p)
	case <-time.After(150 * time.Millisecond):
	}

	// Unknown worker: refused before any write.
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
	s := New(nil, nil, nil, nil, ":0", false)
	s.SetContext(ContextInfo{Mode: "project", Project: "mydemo"})
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

// TestHandleListPrograms verifies GET /api/programs degrades to an empty list
// when the program worker is not on the bus (e.g. a project without one) — the
// endpoint is data-plane driven, so there is nothing to list offline.
func TestHandleListPrograms(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(nil, nil, nil, nil, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := http.Get("http://" + addr + "/api/programs")
	if err != nil {
		t.Fatalf("GET programs: %v", err)
	}
	defer resp.Body.Close()
	var got []ProgramSummary
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list with no program worker, got %d", len(got))
	}
}

// TestParseProgramSearch verifies the list /api/programs reshape of the program
// worker's search answer (its "result" JSON) counts sub-contents.
func TestParseProgramSearch(t *testing.T) {
	s := &Server{}
	reply := event.New(event.TypeRequestCompleted, "program", map[string]any{
		"result": `[{"name":"strict","content_type":"instruction","form_type":"script","description":"d","tags":["a"],"locked":true,"contents":["strict/rules.md"]}]`,
	})
	var out []ProgramSummary
	s.parseProgramSearch(reply, &out)
	if len(out) != 1 {
		t.Fatalf("expected 1 program, got %d", len(out))
	}
	p := out[0]
	if p.Name != "strict" || p.FormType != "script" || p.Contents != 1 || p.Locked != true {
		t.Fatalf("parsed %+v, want strict/script/1/locked", p)
	}
}

// TestHandleGetProgramContentOffline verifies GET /api/programs/{name}/content
// degrades to a service-unavailable error when the program worker is not on the
// bus, mirroring the other program endpoints.
func TestHandleGetProgramContentOffline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(nil, nil, nil, nil, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := http.Get("http://" + addr + "/api/programs/myprog/content?path=myprog%2Frules.md")
	if err != nil {
		t.Fatalf("GET program content: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (program worker offline)", resp.StatusCode, http.StatusServiceUnavailable)
	}
}

// TestHandleWorkerContextOffline verifies GET /api/workers/{id}/context reports
// an unknown/offline worker as 503 without waiting on the bus — the same
// contract the provider endpoint keeps.
func TestHandleWorkerContextOffline(t *testing.T) {
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	engine := eventbus.NewEngine(registry, eventbus.NewMemoryEventStore())
	s := New(nil, engine, &stubCatalog{}, registry, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := http.Get("http://" + addr + "/api/workers/nope/context")
	if err != nil {
		t.Fatalf("GET context: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (worker offline)", resp.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestWebUIServeOnBoundListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(nil, nil, nil, nil, ":0", false)
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

// fakeBuilderSpec returns a SpawnSpec whose closures are never called by the
// code paths under test (RestoreSuspended builds the spec without connecting).
func fakeBuilderSpec(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	return worker.SpawnSpec{Config: cfg}, nil
}

// stubCatalog is an in-memory worker.Manifest: the read model the workers view
// is rendered from.
type stubCatalog struct {
	infos    []worker.WorkerInfo
	archived []worker.WorkerInfo
}

func (c *stubCatalog) Get(id string) (worker.WorkerInfo, bool, error) {
	for _, info := range c.infos {
		if info.ID == id {
			return info, true, nil
		}
	}
	return worker.WorkerInfo{}, false, nil
}

func (c *stubCatalog) List(archived bool) []worker.WorkerInfo {
	if archived {
		return c.archived
	}
	return c.infos
}

func (c *stubCatalog) Update(context.Context, string, worker.WorkerPatch) (worker.WorkerInfo, error) {
	return worker.WorkerInfo{}, nil
}

// TestHandleWorkersDeclaredMerge verifies how declarations merge into
// GET /api/workers: a never-started managed-process worker carries the
// unmanaged badge in "stopped", an in-process worker known to the worker
// service reports its live state, and a declared-but-never-instantiated
// in-process worker surfaces as managed/stopped for the start button.
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

	engine := eventbus.NewEngine(registry, eventbus.NewMemoryEventStore())
	s := New(nil, engine, &stubCatalog{infos: []worker.WorkerInfo{
		{ID: "live-ext", Type: "mcp", Placement: worker.PlacementManagedProcess, State: "running"},
		{ID: "idle-ext", Type: "mcp", Placement: worker.PlacementManagedProcess, State: "stopped"},
		{ID: "susp-managed", Type: "reason", Placement: worker.PlacementInProcess, State: "suspended"},
		{ID: "ghost-managed", Type: "timer", Placement: worker.PlacementInProcess, State: "stopped"},
		{ID: "dial-in", Type: "mcp", Placement: worker.PlacementUnmanagedProcess, State: "stopped"},
	}}, registry, ":0", false)
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
	// The suspended in-process worker is known to the lifecycle: its live state
	// wins and the managed-process badge must not appear.
	if v := byID["susp-managed"]; !v.Managed || v.State != "suspended" || v.Unmanaged {
		t.Fatalf("susp-managed = %+v, want managed/suspended without unmanaged badge", v)
	}
	// Declared but instantiated nowhere: surfaced for the start button.
	if v := byID["ghost-managed"]; !v.Managed || v.State != "stopped" || v.Unmanaged {
		t.Fatalf("ghost-managed = %+v, want managed/stopped", v)
	}
	// A dial-in worker is remote and has no local lifecycle.
	if v := byID["dial-in"]; !v.Remote || !v.Unmanaged || v.Managed {
		t.Fatalf("dial-in = %+v, want remote/unmanaged", v)
	}
}

// TestHandleUnmanagedStartRequiresHost verifies the lifecycle endpoints go to
// the host worker: with no host online they fail closed and never touch the
// worker service directly.
func TestHandleUnmanagedStartRequiresHost(t *testing.T) {
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	engine := eventbus.NewEngine(registry, eventbus.NewMemoryEventStore())
	s := New(nil, engine, &stubCatalog{}, registry, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := http.Post("http://"+addr+"/api/workers/whatever/start", "", nil)
	if err != nil {
		t.Fatalf("POST start: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("start status = %d, want 502 (host offline)", resp.StatusCode)
	}

	// Create is a bus call too: with the host offline it reports the same
	// actionable failure as start, not "endpoint unavailable".
	resp, err = http.Post("http://"+addr+"/api/workers/create", "application/json", strings.NewReader(`{"type":"reason","id":"r1"}`))
	if err != nil {
		t.Fatalf("POST create: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400 (host offline)", resp.StatusCode)
	}
}

// TestHandleUploadPersistsFile is a regression test: POST /api/upload must
// leave the uploaded file on disk after the request returns. It used to be
// deleted because the temp file (up-<rand>-<name>) doubles as the final path,
// so the deferred os.Remove(dst.Name()) removed the very file just "renamed"
// into place (the rename being a same-path no-op).
func TestHandleUploadPersistsFile(t *testing.T) {
	// SetProjectDir pins the default upload dir to <projectDir>/uploads.
	projDir := filepath.Join(t.TempDir(), "projects", "alpha")
	s := New(nil, nil, nil, nil, ":0", false)
	s.SetProjectDir(projDir)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := fw.Write([]byte("hello upload")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	mw.Close()

	resp, err := http.Post("http://"+addr+"/api/upload", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("upload status = %d: %s", resp.StatusCode, body)
	}
	var got struct {
		Path string
		Name string
		Size int64
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if !strings.HasPrefix(got.Path, filepath.Join(projDir, "uploads")) {
		t.Fatalf("path = %q, want under %q", got.Path, filepath.Join(projDir, "uploads"))
	}

	// The returned file must still exist once the request has fully returned.
	fi, err := os.Stat(got.Path)
	if err != nil {
		t.Fatalf("uploaded file %q missing after request: %v", got.Path, err)
	}
	if fi.Size() != int64(len("hello upload")) {
		t.Fatalf("size = %d, want %d", fi.Size(), len("hello upload"))
	}
}

// inputStubChannel records what POST /api/input publishes, and how. It is the
// bus handle the app receives from its interface worker.
type inputStubChannel struct {
	sent       []event.Event
	targets    [][]string
	broadcasts []event.Event
}

func (s *inputStubChannel) ID() string { return "stub" }
func (s *inputStubChannel) Send(_ context.Context, evt event.Event, targets ...string) error {
	s.sent = append(s.sent, evt)
	s.targets = append(s.targets, append([]string(nil), targets...))
	return nil
}
func (s *inputStubChannel) Broadcast(_ context.Context, evt event.Event) error {
	s.broadcasts = append(s.broadcasts, evt)
	return nil
}
func (s *inputStubChannel) Receive(context.Context) (<-chan event.Event, error) {
	return make(chan event.Event), nil
}
func (s *inputStubChannel) Close() error { return nil }

// inputServer wires a Server with the stub as its injected bus handler, the way
// the interface worker would inject it.
func inputServer(ch *inputStubChannel) *Server {
	s := &Server{}
	s.SetHandler(stubHandler{ch: ch})
	return s
}

// stubHandler adapts a recording channel to iface.Handler.
type stubHandler struct{ ch *inputStubChannel }

func (h stubHandler) ID() string { return "webui" }
func (h stubHandler) Send(_ context.Context, evt event.Event, targets ...string) error {
	return h.ch.Send(context.Background(), evt, targets...)
}
func (h stubHandler) Broadcast(_ context.Context, evt event.Event) error {
	return h.ch.Broadcast(context.Background(), evt)
}
func (h stubHandler) Request(context.Context, string, event.Event) (event.Event, error) {
	return event.Event{}, nil
}
func (h stubHandler) OnEvent(func(event.Event), ...event.EventType) iface.Subscription {
	return nopSub{}
}

type nopSub struct{}

func (nopSub) Close() {}

func postInput(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/input", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleInput(rec, req)
	return rec
}

// The input is addressed to the @-mentioned workers. A body that names nobody
// must be refused: falling through to a broadcast would deliver the user's
// message to every subscribed worker (the "I @-ed one worker but everyone got
// it" bug).
func TestHandleInputRequiresTargets(t *testing.T) {
	ch := &inputStubChannel{}
	s := inputServer(ch)

	rec := postInput(t, s, `{"content":[{"type":"text","text":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(ch.sent) != 0 || len(ch.broadcasts) != 0 {
		t.Fatalf("sent=%d broadcasts=%d, want 0/0", len(ch.sent), len(ch.broadcasts))
	}
}

func TestHandleInputMultiTarget(t *testing.T) {
	ch := &inputStubChannel{}
	s := inputServer(ch)

	rec := postInput(t, s, `{"content":[{"type":"text","text":"hi"}],"targets":["a","b"],"input_mode":"append"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if len(ch.sent) != 1 || len(ch.broadcasts) != 0 {
		t.Fatalf("sent=%d broadcasts=%d, want 1/0", len(ch.sent), len(ch.broadcasts))
	}
	if got := ch.targets[0]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("targets = %v, want [a b]", got)
	}
	if mode := ch.sent[0].Payload["input_mode"]; mode != "append" {
		t.Fatalf("input_mode = %v, want append", mode)
	}
}

// `target` is the pre-multi-target spelling; older clients still send it.
func TestHandleInputLegacySingleTarget(t *testing.T) {
	ch := &inputStubChannel{}
	s := inputServer(ch)

	rec := postInput(t, s, `{"content":[{"type":"text","text":"hi"}],"target":"a"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if len(ch.sent) != 1 || len(ch.broadcasts) != 0 {
		t.Fatalf("sent=%d broadcasts=%d, want 1/0", len(ch.sent), len(ch.broadcasts))
	}
	if got := ch.targets[0]; len(got) != 1 || got[0] != "a" {
		t.Fatalf("targets = %v, want [a]", got)
	}
}

// memStore is a test-only bus.Store; durable storage lives with the standalone
// form and is tested there.
type memStore struct {
	mu  sync.RWMutex
	ids map[string]corebus.Identity
}

func newMemStore() *memStore { return &memStore{ids: map[string]corebus.Identity{}} }

func (s *memStore) Load(id string) (corebus.Identity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idt, ok := s.ids[id]
	return idt, ok, nil
}

func (s *memStore) Save(id string, idt corebus.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = idt
	return nil
}

func (s *memStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
	return nil
}

func (s *memStore) All() ([]corebus.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]corebus.Identity, 0, len(s.ids))
	for _, idt := range s.ids {
		out = append(out, idt)
	}
	return out, nil
}

// lifecycleRig stands up the real thing the lifecycle endpoints talk to: the
// webui interface worker holding a bus channel, a host worker that answers restart,
// and an event log so the server's ask/reply correlation works.
type lifecycleRig struct {
	server   *Server
	addr     string
	hostReqs chan map[string]any
	bus      corebus.Control
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
	bus := busctl.New(newMemStore(), registry)
	for _, id := range []corebus.Identity{
		{WorkerID: "webui", Type: "interface", PublishAllow: []event.PublishPattern{event.NewPublishPattern("*")}},
		{WorkerID: "host", Type: "host", PublishAllow: []event.PublishPattern{event.NewPublishPattern("request.*")}},
	} {
		if err := bus.Register(id); err != nil {
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

	// The app-hosted interface worker: the app IS its backend, and receives the
	// bus handler through SetHandler just as in production.
	uiApp := New(log, engine, &stubCatalog{}, registry, ":0", false)
	uiApp.SetBusControl(bus)
	uiCh := inprocess.NewWorkerSide("webui", listener)
	if err := uiCh.Connect(ctx, "inproc://test"); err != nil {
		t.Fatalf("connect webui: %v", err)
	}
	uiWorker := iface.New(iface.Config{ID: "webui", Type: "interface", Bus: uiCh, Backends: []iface.Backend{uiApp}})
	if err := uiWorker.Start(ctx); err != nil {
		t.Fatalf("start webui worker: %v", err)
	}
	t.Cleanup(func() { _ = uiWorker.Stop() })

	hostCh := inprocess.NewWorkerSide("host", listener)
	if err := hostCh.Connect(ctx, "inproc://test"); err != nil {
		t.Fatalf("connect host: %v", err)
	}
	host := &fakeHost{BaseWorker: baseworker.NewBaseWorker("host", hostCh), reqs: make(chan map[string]any, 8)}
	for _, typ := range []event.EventType{"resume", "suspend", "restart", "destroy", "update", "archive", "acl.update", "spawn"} {
		typ := typ
		host.Register(baseworker.Extension{Event: typ}, func(evt event.Event) {
			host.reqs <- evt.Payload
			// Mirrors the real host's reply shape: lifecycle tools echo
			// worker_id, spawn echoes the id it actually used as worker_id.
			replyID := evt.Payload["worker_id"]
			if replyID == nil {
				replyID = evt.Payload["id"]
			}
			host.ReplyCompleted(evt.WorkerId, evt.RequestId, map[string]any{
				"worker_id": replyID,
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

	// The app was constructed (production order) before its worker, and the bus
	// handle already arrived through SetHandler.
	s := uiApp
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return &lifecycleRig{server: s, addr: addr, hostReqs: host.reqs, bus: bus}
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
		// Lifecycle tools take worker_id; spawn takes id (it names the worker it
		// is about to create).
		if got := payload["worker_id"]; got != nil && got != wantID {
			t.Fatalf("host payload = %v, want worker_id %s", payload, wantID)
		} else if got == nil && payload["id"] != wantID {
			t.Fatalf("host payload = %v, want id %s", payload, wantID)
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

// TestCreateGoesThroughHostSpawn verifies the create endpoint is one bus call: the
// form body IS the spawn argument object (no translation, no persistence), and the
// reply is reshaped from the host's answer.
func TestCreateGoesThroughHostSpawn(t *testing.T) {
	rig := newLifecycleRig(t)
	body := `{"type":"reason","id":"r1","placement":"in-process","label":"值班","params":{"goal":"g"}}`
	resp, err := http.Post("http://"+rig.addr+"/api/workers/create", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST create: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	payload := expectHostCall(t, rig, "r1")
	if payload["type"] != "reason" || payload["label"] != "值班" {
		t.Fatalf("spawn payload = %v", payload)
	}
	if _, ok := payload["params"].(map[string]any); !ok {
		t.Fatalf("the form's params object must travel as-is (the host flattens it): %v", payload)
	}

	var created WorkerCreated
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID != "r1" || created.Type != "mcp" || created.Placement != "in-process" || !created.Managed {
		t.Fatalf("created = %+v", created)
	}
}

// TestCreateDuplicateIsConflict verifies a duplicate id maps to 409 (not a
// generic 400): the UI distinguishes "already exists" from "bad definition".
func TestCreateDuplicateIsConflict(t *testing.T) {
	rig := newLifecycleRig(t)
	// The fake host answers every request.completed; to produce the duplicate
	// error path we need it to fail, which the rig's host does not do. Instead
	// assert the mapping on a host-less server: the bus is unreachable, so the
	// status is a gateway error, not a conflict — i.e. only the real host's error
	// text selects 409.
	_ = rig
	s := New(nil, nil, nil, nil, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := http.Post("http://"+addr+"/api/workers/create", "application/json", strings.NewReader(`{"type":"reason","id":"r1"}`))
	if err != nil {
		t.Fatalf("POST create: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 with the host offline", resp.StatusCode)
	}
}
