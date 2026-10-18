package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestFixedModeRegistersOnlyDiscovery verifies a fixed worker offers listing and
// calling but no runtime server mutation or mode switch.
func TestFixedModeRegistersOnlyDiscovery(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeReady, Fixed: true})
	have := map[event.EventType]bool{}
	for _, e := range w.Extensions() {
		have[e.Event] = true
	}
	for _, want := range []event.EventType{EvtServerList, EvtToolList, EvtToolCall} {
		if !have[want] {
			t.Errorf("missing discovery extension %s", want)
		}
	}
	for _, no := range []event.EventType{EvtServerAdd, EvtServerRemove, EvtServerUpdate, EvtExpose} {
		if have[no] {
			t.Errorf("fixed mode must not register %s", no)
		}
	}
}

// TestFixedModeIgnoresSnapshot verifies a fixed worker keeps its configured
// servers and produces no snapshot (config is authoritative).
func TestFixedModeIgnoresSnapshot(t *testing.T) {
	w := New(WorkerConfig{
		ID: "mcp", Bus: channel{}, Fixed: true,
		Servers: []ServerConfig{{Name: "a", URL: "https://a"}},
	})
	snap, err := json.Marshal(snapshotState{
		Servers: []ServerConfig{{Name: "b", URL: "https://b"}},
		Expose:  ExposeReady,
	})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := w.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, ok := w.clients["b"]; ok {
		t.Fatal("fixed mode must ignore a restored server set")
	}
	if _, ok := w.clients["a"]; !ok {
		t.Fatal("fixed mode must keep the configured server")
	}
	if out, err := w.Snapshot(); err != nil || out != nil {
		t.Fatalf("fixed snapshot = %q, %v; want nil,nil", out, err)
	}
}

// TestReadmeStatesAllowedTools verifies the readme's Current section reflects
// the configured duty: the allowlist (or "all") and the mode.
func TestReadmeStatesAllowedTools(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Fixed: true, Expose: ExposeReady,
		AllowedTools: []string{"search", "fetch_content"}})
	text := w.readmeText()
	if !strings.Contains(text, "allowed tools: search, fetch_content") {
		t.Fatalf("readme missing the allowlist:\n%s", text)
	}
	if !strings.Contains(text, "mode: fixed") || !strings.Contains(text, "exposure: ready") {
		t.Fatalf("readme missing the mode/exposure:\n%s", text)
	}

	all := New(WorkerConfig{ID: "mcp", Bus: channel{}})
	if got := all.readmeText(); !strings.Contains(got, "allowed tools: all") {
		t.Fatalf("readme without an allowlist = %q, want 'allowed tools: all'", got)
	}
}

// TestFilterAllowedTools verifies the allowed_tools allowlist filters the tool
// surface (and callability), and that unset means everything passes.
func TestFilterAllowedTools(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, AllowedTools: []string{"a", "c"}})
	got := w.filterAllowed([]toolInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("filtered = %+v, want [a c]", got)
	}
	if w.toolAllowed("b") || !w.toolAllowed("a") {
		t.Fatal("toolAllowed mismatch")
	}

	all := New(WorkerConfig{ID: "mcp", Bus: channel{}})
	if len(all.filterAllowed([]toolInfo{{Name: "x"}})) != 1 || !all.toolAllowed("anything") {
		t.Fatal("unset allowlist must not filter")
	}
}

func TestNewSeedsStaticServers(t *testing.T) {
	w := New(WorkerConfig{
		ID: "mcp", Bus: channel{}, Expose: ExposeAgentic,
		Servers: []ServerConfig{{Name: "a", URL: "https://a"}},
	})
	if _, ok := w.clients["a"]; !ok {
		t.Fatal("static server not seeded into clients")
	}
}

func TestRestoreReplacesStaticBaseline(t *testing.T) {
	// A restored snapshot is authoritative; the constructor's static baseline
	// must not survive it.
	w := New(WorkerConfig{
		ID: "mcp", Bus: channel{}, Expose: ExposeAgentic,
		Servers: []ServerConfig{{Name: "static", URL: "https://static"}},
	})
	snap, err := json.Marshal(snapshotState{Servers: []ServerConfig{{Name: "dyn", URL: "https://dyn"}}})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := w.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, ok := w.clients["dyn"]; !ok {
		t.Fatal("restored server not loaded")
	}
	if _, ok := w.clients["static"]; ok {
		t.Fatal("static baseline incorrectly survived the restore")
	}
}

func TestRegistrationIncludesAgenticAndManagement(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeAgentic})
	have := map[string]bool{}
	for _, e := range w.Extensions() {
		have[string(e.Event)] = true
	}
	for _, evt := range []string{
		string(EvtServerList), string(EvtToolList), string(EvtToolCall),
		string(EvtServerAdd), string(EvtServerRemove), string(EvtServerUpdate),
		string(EvtExpose), string(event.TypeWorkerReadme),
	} {
		if !have[evt] {
			t.Errorf("missing extension %s", evt)
		}
	}
}

// TestReadyModeRegistersAndUnregistersTools verifies ready mode binds one
// extension per cached tool and withdraws it when the tools go away.
func TestReadyModeRegistersAndUnregistersTools(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeReady})
	w.mu.Lock()
	w.clients["srv"] = newServerClient(ServerConfig{Name: "srv", URL: "https://srv"})
	w.setTools("srv", []toolInfo{
		{Name: "doThing", Description: "does a thing", Schema: map[string]any{"type": "object"}},
	})
	w.mu.Unlock()

	evtName := string(toolEventName("srv", "doThing"))
	if !hasExtension(w, evtName) {
		t.Fatal("ready mode did not register mcp.srv.doThing")
	}
	// The registered extension is what peers discover in worker.ready.
	found := false
	for _, e := range w.WatchEntries(false) {
		if e["event"] == evtName {
			found = true
		}
	}
	if !found {
		t.Fatal("registered tool not announced in ready")
	}

	w.mu.Lock()
	w.setTools("srv", nil)
	w.mu.Unlock()
	if hasExtension(w, evtName) {
		t.Fatal("removed tool extension survived")
	}
	for _, e := range w.WatchEntries(false) {
		if e["event"] == evtName {
			t.Fatal("removed tool still announced")
		}
	}
}

// TestAgenticModeDoesNotRegisterPerTool verifies the default mode keeps a fixed
// capability surface even when tool lists are cached.
func TestAgenticModeDoesNotRegisterPerTool(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeAgentic})
	w.mu.Lock()
	w.setTools("srv", []toolInfo{{Name: "doThing"}})
	w.mu.Unlock()
	if hasExtension(w, string(toolEventName("srv", "doThing"))) {
		t.Fatal("agentic mode must not register per-tool extensions")
	}
}

func hasExtension(w *Worker, evt string) bool {
	for _, e := range w.Extensions() {
		if string(e.Event) == evt {
			return true
		}
	}
	return false
}

// TestReadmeTextDescribesWorker verifies the agent-facing guide carries the
// workflow and a snapshot of the current mode and servers.
func TestReadmeTextDescribesWorker(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeReady})
	w.mu.Lock()
	w.clients["srv"] = newServerClient(ServerConfig{Name: "srv", URL: "https://srv"})
	w.setTools("srv", []toolInfo{{Name: "doThing"}})
	w.mu.Unlock()

	text := w.readmeText()
	for _, want := range []string{
		"mcp.server.list", "mcp.tool.list", "mcp.tool.call",
		"exposure: ready", "mode: dynamic", "mcp.server.add", "srv",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("readme missing %q", want)
		}
	}
}

// TestReadmeTextFixedMode verifies the guide reflects a fixed worker (no
// management events).
func TestReadmeTextFixedMode(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Fixed: true})
	text := w.readmeText()
	if !strings.Contains(text, "mode: fixed") || !strings.Contains(text, "management: disabled") {
		t.Fatalf("fixed readme = %q", text)
	}
}

// TestExposeSwitchWithdrawsTools verifies switching to agentic withdraws the
// ready-mode tool extensions while keeping the cached tool list.
func TestExposeSwitchWithdrawsTools(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeReady})
	w.mu.Lock()
	w.clients["srv"] = newServerClient(ServerConfig{Name: "srv", URL: "https://srv"})
	w.setTools("srv", []toolInfo{{Name: "doThing", Description: "does a thing"}})
	w.mu.Unlock()

	evtName := string(toolEventName("srv", "doThing"))
	if !hasExtension(w, evtName) {
		t.Fatal("tool extension not registered in ready mode")
	}

	w.applyExpose(ExposeAgentic)
	if hasExtension(w, evtName) {
		t.Fatal("tool extension survived the switch to agentic")
	}
	w.mu.Lock()
	mode, cached := w.cfg.Expose, len(w.tools["srv"])
	w.mu.Unlock()
	if mode != ExposeAgentic {
		t.Fatalf("mode = %q, want %q", mode, ExposeAgentic)
	}
	if cached != 1 {
		t.Fatalf("tool cache = %d, want 1 (kept for a later re-registration)", cached)
	}
}

// TestHandleExposeSetsAndReadsMode verifies the mcp.expose entry point: a mode
// sets it, and omitting mode reads it back without changing it.
func TestHandleExposeSetsAndReadsMode(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeAgentic})

	set := event.New(EvtExpose, "caller", map[string]any{"mode": ExposeReady})
	set.RequestId = "r1"
	w.handleExpose(set)
	w.mu.Lock()
	got := w.cfg.Expose
	w.mu.Unlock()
	if got != ExposeReady {
		t.Fatalf("mode after set = %q, want %q", got, ExposeReady)
	}

	read := event.New(EvtExpose, "caller", map[string]any{})
	read.RequestId = "r2"
	w.handleExpose(read)
	w.mu.Lock()
	got = w.cfg.Expose
	w.mu.Unlock()
	if got != ExposeReady {
		t.Fatalf("mode after read = %q, want %q", got, ExposeReady)
	}
}

// TestRestoreAppliesPersistedExpose verifies a switched mode rides the snapshot
// and overrides the constructor config on restore.
func TestRestoreAppliesPersistedExpose(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeAgentic})
	snap, err := json.Marshal(snapshotState{Expose: ExposeReady})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := w.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	w.mu.Lock()
	got := w.cfg.Expose
	w.mu.Unlock()
	if got != ExposeReady {
		t.Fatalf("restored expose = %q, want %q", got, ExposeReady)
	}
}

// TestSnapshotIsSortedAndRoundTrips verifies the snapshot is statically ordered
// and that Restore rehydrates the same set.
func TestSnapshotIsSortedAndRoundTrips(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeAgentic})
	w.clients["z"] = newServerClient(ServerConfig{Name: "z", URL: "https://z"})
	w.clients["a"] = newServerClient(ServerConfig{Name: "a", URL: "https://a"})

	snap, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var s snapshotState
	if err := json.Unmarshal(snap, &s); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	want := []ServerConfig{{Name: "a", URL: "https://a"}, {Name: "z", URL: "https://z"}}
	if !reflect.DeepEqual(s.Servers, want) {
		t.Fatalf("snapshot servers = %+v, want %+v", s.Servers, want)
	}

	next := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeAgentic})
	if err := next.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := next.sortedServers(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored = %+v, want %+v", got, want)
	}
}

// TestSnapshotKeepsExplicitEmptySet verifies an empty set is persisted as state
// (not as "no snapshot"), so removing the last server survives a restart
// instead of resurrecting the static baseline.
func TestSnapshotKeepsExplicitEmptySet(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeAgentic})
	snap, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap) == 0 {
		t.Fatal("empty set must still produce a state blob")
	}
	next := New(WorkerConfig{
		ID: "mcp", Bus: channel{}, Expose: ExposeAgentic,
		Servers: []ServerConfig{{Name: "static", URL: "https://static"}},
	})
	if err := next.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(next.clients) != 0 {
		t.Fatalf("restored clients = %v, want empty (static baseline must not return)", next.clients)
	}
}

// TestRemoveSignalsDurableChange verifies a runtime remove drops the server and
// raises the baseworker durable-change signal the host turns into a checkpoint.
func TestRemoveSignalsDurableChange(t *testing.T) {
	var calls atomic.Int32
	w := New(WorkerConfig{
		ID: "mcp", Bus: channel{}, Expose: ExposeAgentic,
		Servers:         []ServerConfig{{Name: "a", URL: "https://a"}},
		OnDurableChange: func() { calls.Add(1) },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.StartDurableLoop(ctx)

	evt := event.New(EvtServerRemove, "caller", map[string]any{"name": "a"})
	evt.RequestId = "req-1"
	w.handleServerRemove(evt)

	if _, ok := w.clients["a"]; ok {
		t.Fatal("server not removed from clients")
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("durable-change signal not delivered")
	}
}
