package provider

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/niq-run/niq/core/itfs/worker"
)

func TestEnsureLLMConfigured(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", cfgPath)

	// No declared workers → passes without any provider.
	if err := EnsureLLMConfigured(nil); err != nil {
		t.Fatalf("empty worker set should pass: %v", err)
	}

	reason := []worker.WorkerConfig{{ID: "niq", Type: "reason"}}

	// Reason worker declared, no provider → error + example seeded.
	if err := EnsureLLMConfigured(reason); err == nil {
		t.Fatal("expected error when reason worker exists without a provider")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("example provider.json not seeded: %v", err)
	}
	if !strings.Contains(mustErrString(EnsureLLMConfigured(reason)), cfgPath) {
		t.Fatalf("error should mention the provider config path")
	}

	// Provider configured → passes.
	if err := Write(&Config{Providers: []Entry{
		{Name: "deepseek", Type: "deepseek", APIKey: "sk-x"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLLMConfigured(reason); err != nil {
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

// TestModelsForPropagatesInputTypes verifies per-model input_types from the
// configured model list flow into ModelInfo.InputTypes, the signal the reason
// worker's image-input gate reads.
func TestModelsForPropagatesInputTypes(t *testing.T) {
	s := &providerSources{}
	entry := Entry{
		Name:   "acme",
		Type:   "openai-compatible",
		APIKey: "sk-test",
		Model:  "texty",
		Models: Models{
			{Name: "texty", InputTypes: []string{"text"}},
			{Name: "visual", InputTypes: []string{"text", "image"}},
			{Name: "unknown"}, // no input_types -> empty (unknown)
		},
	}
	details := s.modelsFor(entry)
	if len(details) != 3 {
		t.Fatalf("details = %d, want 3: %+v", len(details), details)
	}
	if got := details[0].InputTypes; len(got) != 1 || got[0] != "text" {
		t.Errorf("details[0].InputTypes = %v, want [text]", got)
	}
	if got := details[1].InputTypes; len(got) != 2 || got[0] != "text" || got[1] != "image" {
		t.Errorf("details[1].InputTypes = %v, want [text image]", got)
	}
	if got := details[2].InputTypes; len(got) != 0 {
		t.Errorf("details[2].InputTypes = %v, want empty (unknown)", got)
	}
}

// TestInitialProviderInfoNamesOnly verifies a worker selects its provider by
// NAME and its model by name: no endpoint or credential is involved, an empty
// model falls back to the entry's default, and an unknown name resolves to
// nothing (the builder rejects it before a worker exists).
func TestInitialProviderInfoNamesOnly(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", cfgPath)
	if err := Write(&Config{Active: "a", Providers: []Entry{
		{Name: "a", Type: "deepseek", APIKey: "k1", Model: "m1"},
		{Name: "b", Type: "openai-compatible", BaseURL: "https://b.example", APIKey: "k2", Model: "m2"},
	}}); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ provider, model, wantName, wantModel string }{
		{"b", "m2", "b", "m2"}, // explicit pair
		{"b", "", "b", "m2"},   // name only: the entry's default model
		{"", "m1", "a", "m1"},  // model only: config's active provider
		{"", "", "a", "m1"},    // nothing: config default
		{"gone", "m1", "", ""}, // unknown name resolves to nothing
	}
	for _, c := range cases {
		gotName, gotModel := InitialProviderInfo(c.provider, c.model)
		if gotName != c.wantName || gotModel != c.wantModel {
			t.Fatalf("InitialProviderInfo(%q,%q) = %q/%q, want %q/%q",
				c.provider, c.model, gotName, gotModel, c.wantName, c.wantModel)
		}
	}
}
