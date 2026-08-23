package reason

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/54c1/niq/core/event"
	llm "github.com/54c1/niq/core/llm"
)

// fakeSources is a canned ProviderSources used to exercise switching.
type fakeSources struct {
	infos []ProviderInfo
	provs map[string]llm.LLMProvider
}

func (f *fakeSources) Default() llm.LLMProvider {
	if len(f.infos) > 0 {
		if p, ok := f.provs[f.infos[0].Name]; ok {
			return p
		}
	}
	return nil
}
func (f *fakeSources) Build(name, model string) (llm.LLMProvider, error) {
	if name == "" {
		return nil, fmt.Errorf("empty provider name")
	}
	p, ok := f.provs[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return p, nil
}
func (f *fakeSources) List() []ProviderInfo { return f.infos }

func newSwitchableWorker(ch *testChannel) (*BaseReasonWorker, *staticProvider, *staticProvider) {
	pa := &staticProvider{}
	pb := &staticProvider{}
	fake := &fakeSources{
		infos: []ProviderInfo{
			{Name: "a", Default: "m1", Models: []string{"m1", "m2"}},
			{Name: "b", Default: "m3", Models: []string{"m3", "m4"}},
		},
		provs: map[string]llm.LLMProvider{"a": pa, "b": pb},
	}
	w := NewBaseReasonWorker(Config{
		ID:               "r1",
		ProviderSources:  fake,
		Subscriptions:    []event.EventPattern{event.NewPattern("*")},
		Bus:              ch,
	})
	return w, pa, pb
}

// TestSetActiveProviderSwitches verifies setActiveProvider rebinds the active
// provider and the default compactor, and rejects unknown providers/models.
func TestSetActiveProviderSwitches(t *testing.T) {
	ch := newTestChannel()
	w, pa, pb := newSwitchableWorker(ch)

	w.mu.Lock()
	if w.llmProvider != pa {
		t.Fatal("expected initial provider to be the first source")
	}
	if err := w.setActiveProvider("b", ""); err != nil {
		t.Fatalf("switch to b: %v", err)
	}
	if w.llmProvider != pb {
		t.Fatal("active provider did not switch to b")
	}
	if dc, ok := w.compactor.(*DefaultCompactor); !ok || dc.llm != pb {
		t.Fatal("default compactor should be rebound to the new provider")
	}

	if err := w.setActiveProvider("nope", ""); err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if w.llmProvider != pb {
		t.Fatal("active provider changed after failed switch")
	}
	w.mu.Unlock()
}

// TestWorkerUpdateSetProviderEvent drives the worker.update op through the real
// handler and asserts the switch applies and a worker.updated completion is
// broadcast with the explicit model.
func TestWorkerUpdateSetProviderEvent(t *testing.T) {
	ch := newTestChannel()
	w, _, pb := newSwitchableWorker(ch)

	evt := event.New(event.TypeWorkerUpdate, w.ID(), map[string]any{
		"op": "set-llm-provider", "provider": "b", "model": "m3",
	})
	evt.TraceID = "trace-switch"
	w.process(context.Background(), evt)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.llmProvider != pb {
		t.Fatal("provider not switched after worker.update")
	}

	updates := ch.eventsOf(event.TypeWorkerUpdated)
	if len(updates) == 0 {
		t.Fatal("expected a worker.updated completion")
	}
	u := updates[len(updates)-1]
	if u.TraceID != "trace-switch" {
		t.Fatalf("completion trace = %q, want trace-switch", u.TraceID)
	}
	if done, _ := u.Payload["done"].(bool); !done {
		t.Fatalf("set-llm-provider not reported done: %v", u.Payload)
	}
	if p := u.Payload["provider"]; p != "b" {
		t.Fatalf("completion provider = %v, want b", p)
	}
	if m := u.Payload["model"]; m != "m3" {
		t.Fatalf("completion model = %v, want m3", m)
	}
}

// TestWorkerUpdateSetProviderRequiresModel asserts an empty model is rejected
// (no default fallback) and the active provider stays unchanged.
func TestWorkerUpdateSetProviderRequiresModel(t *testing.T) {
	ch := newTestChannel()
	w, pa, _ := newSwitchableWorker(ch)

	evt := event.New(event.TypeWorkerUpdate, w.ID(), map[string]any{
		"op": "set-llm-provider", "provider": "b",
	})
	w.process(context.Background(), evt)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.llmProvider != pa {
		t.Fatal("provider should be unchanged when model is empty")
	}
	updates := ch.eventsOf(event.TypeWorkerUpdated)
	if len(updates) == 0 {
		t.Fatal("expected a worker.updated completion")
	}
	u := updates[len(updates)-1]
	if done, _ := u.Payload["done"].(bool); done {
		t.Fatalf("empty model reported done=true: %v", u.Payload)
	}
	if u.Payload["error"] == "" {
		t.Fatal("expected an error payload for missing model")
	}
}

// TestWorkerUpdateSetProviderRejectsUnknown asserts a failed switch reports
// done=false with an error and leaves the active provider unchanged.
func TestWorkerUpdateSetProviderRejectsUnknown(t *testing.T) {
	ch := newTestChannel()
	w, pa, _ := newSwitchableWorker(ch)

	evt := event.New(event.TypeWorkerUpdate, w.ID(), map[string]any{
		"op": "set-llm-provider", "provider": "does-not-exist", "model": "m9",
	})
	w.process(context.Background(), evt)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.llmProvider != pa {
		t.Fatal("provider should be unchanged after a failed switch")
	}
	updates := ch.eventsOf(event.TypeWorkerUpdated)
	if len(updates) == 0 {
		t.Fatal("expected a worker.updated completion")
	}
	u := updates[len(updates)-1]
	if done, _ := u.Payload["done"].(bool); done {
		t.Fatalf("failed switch reported done=true: %v", u.Payload)
	}
	if u.Payload["error"] == "" {
		t.Fatal("expected an error payload")
	}
}

// TestListLLMProvidersTool asserts the default list_llm_providers tool returns
// the provider table via a normal tool.completed reply.
func TestListLLMProvidersTool(t *testing.T) {
	ch := newTestChannel()
	w, _, _ := newSwitchableWorker(ch)

	w.handleListLLMProvidersTool("call1", "list_llm_providers", "caller", nil)

	comps := ch.eventsOf(event.TypeToolCompleted)
	if len(comps) == 0 {
		t.Fatal("expected a tool.completed reply")
	}
	result, _ := comps[len(comps)-1].Payload["result"].(string)
	if !strings.Contains(result, "\"Name\":\"a\"") || !strings.Contains(result, "\"Name\":\"b\"") {
		t.Fatalf("list_llm_providers did not include both providers: %s", result)
	}
}