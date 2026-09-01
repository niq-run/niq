package provider

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/services/workerhost"
)

// memStore is an in-memory WorkerStore for tests. It avoids importing the
// project package (which owns the file-backed store), since project imports this
// package.
type memStore struct {
	cfgs map[string]worker.WorkerConfig
}

func newMemStore(cfgs ...worker.WorkerConfig) *memStore {
	m := &memStore{cfgs: map[string]worker.WorkerConfig{}}
	for _, c := range cfgs {
		m.cfgs[c.ID] = c
	}
	return m
}

func (m *memStore) SaveConfig(cfg worker.WorkerConfig) error {
	m.cfgs[cfg.ID] = cfg
	return nil
}

func (m *memStore) SaveState(id string, state worker.WorkerState, snapshot []byte) error { return nil }
func (m *memStore) Delete(id string) error                                               { return nil }

func (m *memStore) LoadAll() ([]workerhost.WorkerRecord, error) {
	out := make([]workerhost.WorkerRecord, 0, len(m.cfgs))
	for _, c := range m.cfgs {
		out = append(out, workerhost.WorkerRecord{ID: c.ID, Type: c.Type, Params: c.Params})
	}
	return out, nil
}

// serviceWithWorkers returns a WorkerService backed by an in-memory store
// seeded with the given worker configs.
func serviceWithWorkers(t *testing.T, cfgs ...worker.WorkerConfig) *workerhost.WorkerService {
	t.Helper()
	svc := workerhost.New()
	svc.SetStore(newMemStore(cfgs...))
	return svc
}

func TestEnsureLLMConfigured(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", cfgPath)

	// No persisted workers → passes without any provider.
	if err := EnsureLLMConfigured(serviceWithWorkers(t)); err != nil {
		t.Fatalf("empty store should pass: %v", err)
	}

	reasonSvc := func() *workerhost.WorkerService {
		return serviceWithWorkers(t, worker.WorkerConfig{ID: "niq", Type: "reason"})
	}

	// Reason worker persisted, no provider → error + example seeded.
	if err := EnsureLLMConfigured(reasonSvc()); err == nil {
		t.Fatal("expected error when reason worker exists without a provider")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("example provider.json not seeded: %v", err)
	}
	if !strings.Contains(mustErrString(EnsureLLMConfigured(reasonSvc())), cfgPath) {
		t.Fatalf("error should mention the provider config path")
	}

	// Provider configured → passes.
	if err := Write(&Config{Providers: []Entry{
		{Name: "deepseek", Type: "deepseek", APIKey: "sk-x"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLLMConfigured(reasonSvc()); err != nil {
		t.Fatalf("configured should pass: %v", err)
	}
}

func mustErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// modelsStubServer serves a minimal OpenAI-compatible /models response and
// counts the requests it receives.
func modelsStubServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": [{"id": "api-model-a", "context_window": 4096}, {"id": "api-model-b"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestModelsForConfiguredListSkipsAPI verifies the override contract: a
// non-empty configured model list is served verbatim and the provider's API is
// never queried; with no configured models the API is consulted instead.
func TestModelsForConfiguredListSkipsAPI(t *testing.T) {
	srv, hits := modelsStubServer(t)
	s := &providerSources{}

	configured := Entry{
		Name: "acme", Type: "openai-compatible", BaseURL: srv.URL, APIKey: "sk-test",
		Model:         "m1",
		Models:        Models{{Name: "m1", ContextWindow: 8192}, {Name: "m2"}},
		ContextWindow: 1024,
	}
	details := s.modelsFor(configured)
	if got := hits.Load(); got != 0 {
		t.Fatalf("configured models: API hit %d times, want 0", got)
	}
	if len(details) != 2 {
		t.Fatalf("configured models: got %d details, want 2: %+v", len(details), details)
	}
	if details[0].ID != "m1" || details[0].ContextWindow != 8192 {
		t.Fatalf("configured models: details[0] = %+v, want m1/8192", details[0])
	}
	// m2 has no per-model window: falls back to the provider's configured one.
	if details[1].ID != "m2" || details[1].ContextWindow != 1024 {
		t.Fatalf("configured models: details[1] = %+v, want m2/1024", details[1])
	}

	// No configured models: the API is the only source.
	discovered := Entry{
		Name: "acme", Type: "openai-compatible", BaseURL: srv.URL, APIKey: "sk-test",
		Model: "m1", ContextWindow: 1024,
	}
	details = s.modelsFor(discovered)
	if got := hits.Load(); got != 1 {
		t.Fatalf("no configured models: API hit %d times, want 1", got)
	}
	if len(details) != 2 || details[0].ID != "api-model-a" || details[0].ContextWindow != 4096 {
		t.Fatalf("no configured models: details = %+v, want api-model-a/4096 first", details)
	}
	// api-model-b reports no window: falls back to the provider's configured one.
	if details[1].ID != "api-model-b" || details[1].ContextWindow != 1024 {
		t.Fatalf("no configured models: details[1] = %+v, want api-model-b/1024", details[1])
	}
}
