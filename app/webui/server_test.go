package webui

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
	"testing"
	"time"

	"github.com/niq-run/niq/core/impl/eventbus"
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

// TestHandleUpdateAllow verifies PUT /api/workers/{id}/allow edits the bus
// registry: subscribe_allow is replaced wholesale (source restriction kept),
// a missing publish_allow keeps the current value, and unknown workers 404.
// TestHandleUpdateAllowGoesThroughHost verifies an ACL edit is one bus call to the
// host's acl.update tool. The WebUI no longer touches the registry or any file:
// it only normalizes the request (an absent list keeps the current value, an
// omitted subscription source becomes the any-source wildcard).
func TestHandleUpdateAllowGoesThroughHost(t *testing.T) {
	rig := newLifecycleRig(t)
	// The rig's registry holds webui and host; add the worker being edited so
	// the "keep the other list" path has a current value to keep.
	t.Cleanup(func() {})

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
	payload := expectHostCall(t, rig, "webui")
	if payload["worker_id"] != "webui" {
		t.Fatalf("payload = %v", payload)
	}
	// publish_allow was omitted, so the current grants travel unchanged.
	if pubs, ok := payload["publish_allow"].([]any); !ok || len(pubs) == 0 {
		t.Fatalf("publish_allow = %v, want the current list", payload["publish_allow"])
	}
	subs, ok := payload["subscribe_allow"].([]any)
	if !ok || len(subs) != 2 {
		t.Fatalf("subscribe_allow = %v", payload["subscribe_allow"])
	}
	first, _ := subs[0].(map[string]any)
	if first["type"] != "request.completed" || first["source"] != "timer" {
		t.Fatalf("first grant = %v", first)
	}
	second, _ := subs[1].(map[string]any)
	if second["type"] != "worker.ready" || second["source"] != "*" {
		t.Fatalf("an omitted source must be stored as the any-source wildcard: %v", second)
	}

	// Unknown worker: refused before any bus call.
	resp, err = put("nope", `{"subscribe_allow":[]}`)
	if err != nil {
		t.Fatalf("PUT unknown: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown worker status = %d, want 404", resp.StatusCode)
	}
	select {
	case p := <-rig.hostReqs:
		t.Fatalf("unknown worker reached the host: %v", p)
	case <-time.After(150 * time.Millisecond):
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

func TestBasicAuthNonLoopback(t *testing.T) {
	s := New(nil, nil, nil, nil, ":0", false)
	s.SetBasicAuth("alice", "s3cret")

	hit := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true })
	h := s.basicAuth(next)

	// Loopback peers are not challenged even without credentials.
	hit = false
	req := httptest.NewRequest("GET", "/api/context", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !hit || rr.Code != http.StatusOK {
		t.Fatalf("loopback: hit=%v code=%d, want hit with 200", hit, rr.Code)
	}

	// Non-loopback without credentials → 401 + WWW-Authenticate.
	hit = false
	req = httptest.NewRequest("GET", "/api/context", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if hit || rr.Code != http.StatusUnauthorized {
		t.Fatalf("no-cred: hit=%v code=%d, want reject with 401", hit, rr.Code)
	}
	if rr.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("no-cred: missing WWW-Authenticate header")
	}

	// Non-loopback with the right credentials → allowed.
	hit = false
	req = httptest.NewRequest("GET", "/api/context", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.SetBasicAuth("alice", "s3cret")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !hit || rr.Code != http.StatusOK {
		t.Fatalf("good-cred: hit=%v code=%d, want hit with 200", hit, rr.Code)
	}

	// Wrong password → 401.
	hit = false
	req = httptest.NewRequest("GET", "/api/context", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.SetBasicAuth("alice", "nope")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if hit || rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad-cred: hit=%v code=%d, want reject with 401", hit, rr.Code)
	}
}

// TestBasicAuthUnconfiguredPassesThrough asserts auth is opt-in: with no
// credentials configured the gate lets every caller through rather than forcing
// a login the operator never asked for. Whether to expose an unauthenticated
// server is the operator's call (the bind address is their choice), not the
// server's.
func TestBasicAuthUnconfiguredPassesThrough(t *testing.T) {
	s := New(nil, nil, nil, nil, ":0", false) // no credentials configured
	hit := false
	h := s.basicAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))

	for _, remote := range []string{"127.0.0.1:5555", "203.0.113.7:5555"} {
		hit = false
		req := httptest.NewRequest("GET", "/api/context", nil)
		req.RemoteAddr = remote
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if !hit || rr.Code != http.StatusOK {
			t.Fatalf("peer %s: hit=%v code=%d, want pass-through 200", remote, hit, rr.Code)
		}
	}
}

// TestBasicAuthAppliesAfterNew asserts credentials configured *after* New()
// gate the assembled handler. The project WebUI is built by New() and then
// configured by setters (run.go calls SetBasicAuth once the server exists), so
// a handler that captured the credentials at construction time would silently
// serve remote peers without auth on a non-loopback bind.
func TestBasicAuthAppliesAfterNew(t *testing.T) {
	s := New(nil, nil, nil, nil, ":0", false)
	s.SetContext(ContextInfo{Mode: "project", Project: "mydemo"})
	s.SetBasicAuth("alice", "s3cret")

	// Non-loopback without credentials → 401 + challenge.
	req := httptest.NewRequest("GET", "/api/context", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	rr := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no-cred code=%d, want 401", rr.Code)
	}
	if rr.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("no-cred: missing WWW-Authenticate header")
	}

	// With the configured credentials → served.
	req = httptest.NewRequest("GET", "/api/context", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.SetBasicAuth("alice", "s3cret")
	rr = httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("good-cred code=%d, want 200", rr.Code)
	}
	var ci ContextInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &ci); err != nil {
		t.Fatalf("decode context: %v", err)
	}
	if ci.Mode != "project" || ci.Project != "mydemo" {
		t.Fatalf("context=%+v, want project/mydemo", ci)
	}
}

func TestParseAuthSpec(t *testing.T) {
	for _, tc := range []struct {
		spec, wantUser, wantPass string
		wantOK                   bool
	}{
		{"", "", "", false},
		{"  ", "", "", false},
		{"s3cret", "niq", "s3cret", true},
		{"alice:s3cret", "alice", "s3cret", true},
		{":s3cret", "", "", false},
		{"alice:", "", "", false},
	} {
		u, p, ok := ParseAuthSpec(tc.spec)
		if u != tc.wantUser || p != tc.wantPass || ok != tc.wantOK {
			t.Fatalf("ParseAuthSpec(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.spec, u, p, ok, tc.wantUser, tc.wantPass, tc.wantOK)
		}
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

// stubCatalog is an in-memory worker.Catalog: the read model the workers view
// is rendered from.
type stubCatalog struct {
	infos    []worker.WorkerInfo
	archived []string
	decls    map[string]worker.Decl
}

func (c *stubCatalog) Get(id string) (worker.WorkerInfo, bool, error) {
	for _, info := range c.infos {
		if info.ID == id {
			return info, true, nil
		}
	}
	return worker.WorkerInfo{}, false, nil
}

func (c *stubCatalog) List() []worker.WorkerInfo { return c.infos }

func (c *stubCatalog) Archived() []string { return c.archived }

func (c *stubCatalog) Declarations() map[string]worker.Decl { return c.decls }

// archived is the archived-id set the archive panel renders.
type stubCatalogArchived = []string

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
