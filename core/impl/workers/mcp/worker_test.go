package mcp

import (
	"reflect"
	"testing"
)

// memStore is an in-memory ServerStore for tests.
type memStore struct {
	servers []ServerConfig
	saves   int
}

func (m *memStore) Load() ([]ServerConfig, error) { return m.servers, nil }
func (m *memStore) Save(s []ServerConfig) error {
	m.servers = append([]ServerConfig{}, s...)
	m.saves++
	return nil
}

func TestNewSeedsStaticServersToStore(t *testing.T) {
	store := &memStore{}
	w := New(WorkerConfig{
		ID:    "mcp",
		Bus:   channel{},
		Expose: ExposeAgentic,
		Servers: []ServerConfig{{Name: "a", URL: "https://a"}},
		Store: store,
	})
	if store.saves != 1 {
		t.Fatalf("store saves = %d, want 1 (static baseline seeded)", store.saves)
	}
	if _, ok := w.clients["a"]; !ok {
		t.Fatal("static server not seeded into clients")
	}
}

func TestStoreIsAuthoritativeWhenPresent(t *testing.T) {
	// Store already has a dynamic-only set; static baseline must not override it.
	store := &memStore{servers: []ServerConfig{{Name: "dyn", URL: "https://dyn"}}}
	w := New(WorkerConfig{
		ID: "mcp", Bus: channel{}, Expose: ExposeAgentic,
		Servers: []ServerConfig{{Name: "static", URL: "https://static"}},
		Store:   store,
	})
	if _, ok := w.clients["dyn"]; !ok {
		t.Fatal("persisted server not loaded")
	}
	if _, ok := w.clients["static"]; ok {
		t.Fatal("static baseline incorrectly overrode persisted store")
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
	} {
		if !have[evt] {
			t.Errorf("missing extension %s", evt)
		}
	}
}

func TestReadyModeRegistersPerToolExtensions(t *testing.T) {
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeReady})
	w.started = true
	w.clients["srv"] = newServerClient(ServerConfig{Name: "srv", URL: "https://srv"})
	w.registerServerTools("srv", []toolInfo{
		{Name: "doThing", Description: "does a thing", Schema: map[string]any{"type": "object"}},
	})
	found := false
	for _, e := range w.Extensions() {
		if string(e.Event) == "mcp.srv.doThing" {
			found = true
		}
	}
	if !found {
		t.Fatal("ready mode did not register mcp.srv.doThing extension")
	}
}

func TestPersistWritesSortedServers(t *testing.T) {
	store := &memStore{}
	w := New(WorkerConfig{ID: "mcp", Bus: channel{}, Expose: ExposeAgentic, Store: store})
	w.clients["z"] = newServerClient(ServerConfig{Name: "z", URL: "https://z"})
	w.clients["a"] = newServerClient(ServerConfig{Name: "a", URL: "https://a"})
	w.persist()
	if store.saves != 1 {
		t.Fatalf("persist saves = %d, want 1", store.saves)
	}
	want := []ServerConfig{{Name: "a", URL: "https://a"}, {Name: "z", URL: "https://z"}}
	if !reflect.DeepEqual(w.sortedServers(), want) {
		t.Fatalf("sortedServers = %+v, want %+v", w.sortedServers(), want)
	}
	if !reflect.DeepEqual(store.servers, want) {
		t.Fatalf("persisted = %+v, want %+v", store.servers, want)
	}
}