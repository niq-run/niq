package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/service/workerhost"
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
