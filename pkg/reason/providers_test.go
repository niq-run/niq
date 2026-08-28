package reason

import (
	"context"
	"encoding/json"
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
		ID:              "r1",
		ProviderSources: fake,
		Subscriptions:   []event.EventPattern{event.NewPattern("*")},
		Bus:             ch,
	})
	return w, pa, pb
}

// TestSnapshotRoundTripPreservesProvider verifies a runtime provider switch
// survives a restart: the snapshot carries the selection and Restore rebinds
// the worker to it, rather than silently reverting to the configured default.
func TestSnapshotRoundTripPreservesProvider(t *testing.T) {
	w, _, _ := newSwitchableWorker(newTestChannel())
	w.transcript.Apply(InputPatch{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hello"}}},
	}})
	w.mu.Lock()
	err := w.setActiveProvider("b", "m4")
	w.mu.Unlock()
	if err != nil {
		t.Fatalf("setActiveProvider: %v", err)
	}

	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// A fresh worker stands in for the process restart; it builds its own
	// provider instances, so compare against its pb rather than the original's.
	restored, _, pb2 := newSwitchableWorker(newTestChannel())
	if err := restored.Restore(blob); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.providerName != "b" || restored.providerModel != "m4" {
		t.Fatalf("restored selection = %s/%s, want b/m4", restored.providerName, restored.providerModel)
	}
	if restored.llmProvider != pb2 {
		t.Fatal("restored worker was not rebound to provider b")
	}
	msgs := restored.transcript.Render()
	if len(msgs) != 1 || msgs[0].Content[0].Text != "hello" {
		t.Fatalf("transcript not restored: %+v", msgs)
	}
}

// TestRestoreStaleProviderKeepsDefault verifies a snapshot naming a provider
// that is no longer configured does not take the worker down: Restore logs it
// and keeps the provider resolved at construction.
func TestRestoreStaleProviderKeepsDefault(t *testing.T) {
	blob := []byte(`{"transcript":{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]},` +
		`"provider":{"name":"gone","model":"m9"}}`)
	w, pa, _ := newSwitchableWorker(newTestChannel())
	if err := w.Restore(blob); err != nil {
		t.Fatalf("Restore with a stale selection: %v", err)
	}
	if w.llmProvider != pa {
		t.Fatal("stale selection should leave the constructed provider in place")
	}
	if w.providerName != "" || w.providerModel != "" {
		t.Fatalf("selection = %s/%s, want the constructed default", w.providerName, w.providerModel)
	}
	msgs := w.transcript.Render()
	if len(msgs) != 1 || msgs[0].Content[0].Text != "hi" {
		t.Fatalf("transcript not restored alongside a stale selection: %+v", msgs)
	}
}

// TestSetActiveProviderSwitches verifies setActiveProvider rebinds the active
// provider and the default compactor, records the current name/model, and
// rejects unknown providers/models.
func TestSetActiveProviderSwitches(t *testing.T) {
	ch := newTestChannel()
	w, pa, pb := newSwitchableWorker(ch)

	w.mu.Lock()
	if w.llmProvider != pa {
		t.Fatal("expected initial provider to be the first source")
	}
	if err := w.setActiveProvider("b", "m3"); err != nil {
		t.Fatalf("switch to b: %v", err)
	}
	if w.llmProvider != pb {
		t.Fatal("active provider did not switch to b")
	}
	if w.providerName != "b" || w.providerModel != "m3" {
		t.Fatalf("current provider = %s/%s, want b/m3", w.providerName, w.providerModel)
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

// TestUpdateRequestedSetProviderEvent drives the worker.update
// op=provider through the real handler and asserts the switch applies and a
// worker.updated completion is broadcast with the explicit model.
func TestUpdateRequestedSetProviderEvent(t *testing.T) {
	ch := newTestChannel()
	w, _, pb := newSwitchableWorker(ch)

	evt := event.New(event.TypeWorkerUpdate, w.ID(), map[string]any{
		"op": "provider.switch", "provider": "b", "model": "m3",
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
		t.Fatalf("provider switch not reported done: %v", u.Payload)
	}
	if p := u.Payload["provider"]; p != "b" {
		t.Fatalf("completion provider = %v, want b", p)
	}
	if m := u.Payload["model"]; m != "m3" {
		t.Fatalf("completion model = %v, want m3", m)
	}
}

// TestUpdateRequestedSetProviderRequiresModel asserts an empty model is
// rejected (no default fallback) and the active provider stays unchanged.
func TestUpdateRequestedSetProviderRequiresModel(t *testing.T) {
	ch := newTestChannel()
	w, pa, _ := newSwitchableWorker(ch)

	evt := event.New(event.TypeWorkerUpdate, w.ID(), map[string]any{
		"op": "provider.switch", "provider": "b",
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

// TestUpdateRequestedSetProviderRejectsUnknown asserts a failed switch reports
// done=false with an error and leaves the active provider unchanged.
func TestUpdateRequestedSetProviderRejectsUnknown(t *testing.T) {
	ch := newTestChannel()
	w, pa, _ := newSwitchableWorker(ch)

	evt := event.New(event.TypeWorkerUpdate, w.ID(), map[string]any{
		"op": "provider.switch", "provider": "does-not-exist", "model": "m9",
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

// TestStatusQueryProviders asserts worker.query op=provider.list returns a
// worker.status snapshot carrying the provider table and the current choice.
func TestStatusQueryProviders(t *testing.T) {
	ch := newTestChannel()
	w, _, _ := newSwitchableWorker(ch)

	evt := event.New(event.TypeWorkerQuery, w.ID(), map[string]any{
		"subject": "provider.list",
	})
	evt.TraceID = "trace-status"
	w.process(context.Background(), evt)

	statuses := ch.eventsOf(event.TypeWorkerStatus)
	if len(statuses) == 0 {
		t.Fatal("expected a worker.status snapshot")
	}
	s := statuses[len(statuses)-1]
	if s.TraceID != "trace-status" {
		t.Fatalf("status trace = %q, want trace-status", s.TraceID)
	}
	if s.Payload["subject"] != "provider.list" {
		t.Fatalf("status subject = %v, want provider.list", s.Payload["op"])
	}
	b, _ := json.Marshal(s.Payload["providers"])
	if !strings.Contains(string(b), `"name":"a"`) || !strings.Contains(string(b), `"name":"b"`) {
		t.Fatalf("status providers did not include both providers: %s", b)
	}
	current, _ := s.Payload["current"].(map[string]any)
	if current["provider"] != "" {
		t.Fatalf("initial current provider = %v, want empty", current["provider"])
	}
}

// TestStatusQueryCurrent asserts worker.query op=provider.current returns just
// the current provider/model, reflecting a prior provider.switch.
func TestStatusQueryCurrent(t *testing.T) {
	ch := newTestChannel()
	w, _, _ := newSwitchableWorker(ch)

	w.mu.Lock()
	_ = w.setActiveProvider("b", "m3")
	w.mu.Unlock()

	evt := event.New(event.TypeWorkerQuery, w.ID(), map[string]any{
		"subject": "provider.current",
	})
	w.process(context.Background(), evt)

	statuses := ch.eventsOf(event.TypeWorkerStatus)
	if len(statuses) == 0 {
		t.Fatal("expected a worker.status snapshot")
	}
	s := statuses[len(statuses)-1]
	if s.Payload["subject"] != "provider.current" {
		t.Fatalf("status subject = %v, want provider.current", s.Payload["op"])
	}
	if s.Payload["provider"] != "b" || s.Payload["model"] != "m3" {
		t.Fatalf("current = %v/%v, want b/m3", s.Payload["provider"], s.Payload["model"])
	}
}
