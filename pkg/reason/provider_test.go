package reason

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	llm "github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/pkg/reason/transcript"
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
		Bus:             ch,
	})
	return w, pa, pb
}

// newSwitchableWorkerWithHook builds a started switchable worker whose
// durable-change callback counts its invocations, and returns a reader.
func newSwitchableWorkerWithHook(t *testing.T) (*BaseReasonWorker, *testChannel, func() int) {
	t.Helper()
	pa := &staticProvider{}
	pb := &staticProvider{}
	fake := &fakeSources{
		infos: []ProviderInfo{
			{Name: "a", Default: "m1", Models: []string{"m1", "m2"}},
			{Name: "b", Default: "m3", Models: []string{"m3", "m4"}},
		},
		provs: map[string]llm.LLMProvider{"a": pa, "b": pb},
	}
	var calls atomic.Int32
	ch := newTestChannel()
	w := NewBaseReasonWorker(Config{
		ID:              "r1",
		ProviderSources: fake,
		Bus:             ch,
		OnDurableChange: func() { calls.Add(1) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop(); cancel() })
	return w, ch, func() int { return int(calls.Load()) }
}

// TestProviderSwitchPersists verifies a successful provider.switch notifies the
// durable-change callback, so the embedding layer can checkpoint the choice
// instead of leaving it in memory until suspend/shutdown.
func TestProviderSwitchPersists(t *testing.T) {
	w, ch, calls := newSwitchableWorkerWithHook(t)

	ch.in <- event.New(TypeProviderSwitch, w.ID(), map[string]any{
		"provider": "b", "model": "m3",
	})
	waitCond(t, testTimeout, func() bool { return calls() > 0 }, "durable-change callback")

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.providerName != "b" || w.providerModel != "m3" {
		t.Fatalf("selection = %s/%s, want b/m3", w.providerName, w.providerModel)
	}
}

// TestFailedProviderSwitchDoesNotPersist verifies a rejected switch (missing
// model) leaves the callback alone — nothing durable changed, and the switch
// itself is already reported through the request.failed reply.
func TestFailedProviderSwitchDoesNotPersist(t *testing.T) {
	w, ch, calls := newSwitchableWorkerWithHook(t)

	ch.in <- event.New(TypeProviderSwitch, w.ID(), map[string]any{
		"provider": "b",
	})
	waitCond(t, testTimeout, func() bool {
		return len(ch.eventsOf(event.TypeRequestFailed)) > 0
	}, "request.failed reply")

	// Give a would-be callback time to arrive before declaring it absent.
	time.Sleep(50 * time.Millisecond)
	if n := calls(); n != 0 {
		t.Fatalf("callback invoked %d time(s) after a rejected switch", n)
	}
}

// TestSnapshotRoundTripPreservesProvider verifies a runtime provider switch
// survives a restart: the snapshot carries the selection and Restore rebinds
// the worker to it, rather than silently reverting to the configured default.
func TestSnapshotRoundTripPreservesProvider(t *testing.T) {
	w, _, _ := newSwitchableWorker(newTestChannel())
	w.transcript.Apply(transcript.InputPatch{Messages: []llm.Message{
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

// TestSetActiveProviderSwitches verifies setActiveProvider switches the active
// provider (which the compactor reads lazily, so no rebinding is needed),
// records the current name/model, and rejects unknown providers/models.
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
	// The compactor is wired once (by NewWorker) and reads the active provider
	// via LLMProvider() on every call, so a switch needs no rebinding here.

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

	evt := event.New(TypeProviderSwitch, w.ID(), map[string]any{
		"provider": "b", "model": "m3",
	})
	evt.TraceID = "trace-switch"
	w.process(context.Background(), evt)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.llmProvider != pb {
		t.Fatal("provider not switched after provider.switch")
	}

	updates := ch.eventsOf(event.TypeRequestCompleted)
	if len(updates) == 0 {
		t.Fatal("expected a request.completed reply")
	}
	u := updates[len(updates)-1]
	if u.TraceID != "trace-switch" {
		t.Fatalf("completion trace = %q, want trace-switch", u.TraceID)
	}
	if res, _ := u.Payload["result"].(string); res != "switched to provider b model m3" {
		t.Fatalf("completion result = %q, want switched to provider b model m3", res)
	}
}

// TestUpdateRequestedSetProviderRequiresModel asserts an empty model is
// rejected (no default fallback) and the active provider stays unchanged.
func TestUpdateRequestedSetProviderRequiresModel(t *testing.T) {
	ch := newTestChannel()
	w, pa, _ := newSwitchableWorker(ch)

	evt := event.New(TypeProviderSwitch, w.ID(), map[string]any{
		"provider": "b",
	})
	w.process(context.Background(), evt)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.llmProvider != pa {
		t.Fatal("provider should be unchanged when model is empty")
	}
	updates := ch.eventsOf(event.TypeRequestFailed)
	if len(updates) == 0 {
		t.Fatal("expected a request.failed reply")
	}
	u := updates[len(updates)-1]
	if u.Payload["error"] == "" {
		t.Fatal("expected an error payload for missing model")
	}
}

// TestUpdateRequestedSetProviderRejectsUnknown asserts a failed switch reports
// an error and leaves the active provider unchanged.
func TestUpdateRequestedSetProviderRejectsUnknown(t *testing.T) {
	ch := newTestChannel()
	w, pa, _ := newSwitchableWorker(ch)

	evt := event.New(TypeProviderSwitch, w.ID(), map[string]any{
		"provider": "does-not-exist", "model": "m9",
	})
	w.process(context.Background(), evt)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.llmProvider != pa {
		t.Fatal("provider should be unchanged after a failed switch")
	}
	updates := ch.eventsOf(event.TypeRequestFailed)
	if len(updates) == 0 {
		t.Fatal("expected a request.failed reply")
	}
	u := updates[len(updates)-1]
	if u.Payload["error"] == "" {
		t.Fatal("expected an error payload")
	}
}

// TestStatusQueryProviders asserts a provider.list request returns a
// request.completed snapshot carrying the provider table and the current choice.
func TestStatusQueryProviders(t *testing.T) {
	ch := newTestChannel()
	w, _, _ := newSwitchableWorker(ch)

	evt := event.New(TypeProviderList, w.ID(), nil)
	evt.TraceID = "trace-status"
	w.process(context.Background(), evt)

	statuses := ch.eventsOf(event.TypeRequestCompleted)
	if len(statuses) == 0 {
		t.Fatal("expected a request.completed snapshot")
	}
	s := statuses[len(statuses)-1]
	if s.TraceID != "trace-status" {
		t.Fatalf("status trace = %q, want trace-status", s.TraceID)
	}
	// The snapshot travels as a JSON string in the reply's "result" field.
	var snapshot struct {
		Providers []map[string]any `json:"providers"`
		Current   map[string]any   `json:"current"`
	}
	res, _ := s.Payload["result"].(string)
	if err := json.Unmarshal([]byte(res), &snapshot); err != nil {
		t.Fatalf("status result is not valid JSON: %v (result=%q)", err, res)
	}
	b, _ := json.Marshal(snapshot.Providers)
	if !strings.Contains(string(b), `"name":"a"`) || !strings.Contains(string(b), `"name":"b"`) {
		t.Fatalf("status providers did not include both providers: %s", b)
	}
	if snapshot.Current["provider"] != "" {
		t.Fatalf("initial current provider = %v, want empty", snapshot.Current["provider"])
	}
}

// TestStatusQueryCurrent asserts a provider.current request returns just the
// current provider/model, reflecting a prior provider.switch.
func TestStatusQueryCurrent(t *testing.T) {
	ch := newTestChannel()
	w, _, _ := newSwitchableWorker(ch)

	w.mu.Lock()
	_ = w.setActiveProvider("b", "m3")
	w.mu.Unlock()

	evt := event.New(TypeProviderCurrent, w.ID(), nil)
	w.process(context.Background(), evt)

	statuses := ch.eventsOf(event.TypeRequestCompleted)
	if len(statuses) == 0 {
		t.Fatal("expected a request.completed snapshot")
	}
	s := statuses[len(statuses)-1]
	// The snapshot travels as a JSON string in the reply's "result" field.
	var current struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	res, _ := s.Payload["result"].(string)
	if err := json.Unmarshal([]byte(res), &current); err != nil {
		t.Fatalf("current result is not valid JSON: %v (result=%q)", err, res)
	}
	if current.Provider != "b" || current.Model != "m3" {
		t.Fatalf("current = %v/%v, want b/m3", current.Provider, current.Model)
	}
}
