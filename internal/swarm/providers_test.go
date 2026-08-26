package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/pkg/providercfg"
	"github.com/54c1/niq/pkg/service/workerhost"
)

// reasonStoreSvc returns a WorkerService over a store that has one persisted
// reason worker.
func reasonStoreSvc(t *testing.T) *workerhost.WorkerService {
	t.Helper()
	store, err := NewFileWorkerStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.SaveConfig(worker.WorkerConfig{ID: "niq", Type: "reason"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	svc := workerhost.New()
	svc.SetStore(store)
	return svc
}

func TestEnsureLLMConfigured(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", cfgPath)

	// No persisted workers → passes without any provider.
	emptyStore, err := NewFileWorkerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	emptySvc := workerhost.New()
	emptySvc.SetStore(emptyStore)
	if err := ensureLLMConfigured(emptySvc); err != nil {
		t.Fatalf("empty store should pass: %v", err)
	}

	// Reason worker persisted, no provider → error + example seeded.
	if err := ensureLLMConfigured(reasonStoreSvc(t)); err == nil {
		t.Fatal("expected error when reason worker exists without a provider")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("example provider.json not seeded: %v", err)
	}
	if !strings.Contains(mustErrString(ensureLLMConfigured(reasonStoreSvc(t))), cfgPath) {
		t.Fatalf("error should mention the provider config path")
	}

	// Provider configured → passes.
	if err := providercfg.Write(&providercfg.Config{Providers: []providercfg.Provider{
		{Name: "deepseek", Type: "deepseek", APIKey: "sk-x"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := ensureLLMConfigured(reasonStoreSvc(t)); err != nil {
		t.Fatalf("configured should pass: %v", err)
	}
}

func mustErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
