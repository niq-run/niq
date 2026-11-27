package reason

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/niq-run/niq/core/impl/reason/transcript"
	"github.com/niq-run/niq/core/itfs/event"
	llm "github.com/niq-run/niq/core/itfs/llm"
)

// TestContextStatusQuery verifies context.status answers with the round's
// accounting, the prompt composition rescaled to it, the live transcript size
// and the budgets — the same numbers reason.response reports, from one source.
func TestContextStatusQuery(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(&staticProvider{}, ch)
	cached := 900

	w.transcript.Apply(transcript.InputPatch{Messages: []llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: string(make([]byte, 400))}},
	}}})
	w.mu.Lock()
	w.contextWindow = 1000
	w.roundBreakdown = []contextSection{
		{Key: breakdownSystem, Tokens: 400},
		{Key: breakdownMessagesUser, Tokens: 600},
	}
	w.recordContextUsage(llm.Message{Usage: &llm.Usage{
		InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050, CacheReadTokens: &cached,
	}})
	w.mu.Unlock()

	evt := event.New(TypeContextStatus, "caller", nil)
	evt.RequestId = "call-1"
	evt.TraceID = "trace-1"
	w.process(context.Background(), evt)

	replies := ch.eventsOf(event.TypeRequestCompleted)
	if len(replies) != 1 {
		t.Fatalf("expected one request.completed reply, got %d", len(replies))
	}
	if replies[0].TraceID != "trace-1" || replies[0].RequestId != "call-1" {
		t.Fatalf("reply did not echo the request: %+v", replies[0])
	}
	b, err := json.Marshal(replies[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		ContextWindow    int     `json:"context_window"`
		OccupancyPercent float64 `json:"occupancy_percent"`
		UsedTokens       int     `json:"used_tokens"`
		InputTokens      int     `json:"input_tokens"`
		CacheReadTokens  int     `json:"cache_read_tokens"`
		CacheHitPercent  float64 `json:"cache_hit_percent"`
		Rounds           uint64  `json:"rounds"`
		TranscriptTokens int     `json:"transcript_tokens"`
		Messages         int     `json:"messages"`
		BudgetSoft       float64 `json:"budget_soft"`
		BudgetHard       float64 `json:"budget_hard"`
		Breakdown        []struct {
			Key    string `json:"key"`
			Tokens int    `json:"tokens"`
		} `json:"breakdown"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatalf("snapshot is not valid JSON: %v (payload=%s)", err, b)
	}

	if snap.ContextWindow != 1000 || snap.UsedTokens != 1050 || snap.Rounds != 1 {
		t.Fatalf("usage fields = %+v", snap)
	}
	if snap.OccupancyPercent != 105 {
		t.Fatalf("occupancy = %v, want 105", snap.OccupancyPercent)
	}
	if snap.CacheReadTokens != 900 || snap.CacheHitPercent != 90 {
		t.Fatalf("cache fields = %d / %v, want 900 / 90", snap.CacheReadTokens, snap.CacheHitPercent)
	}
	if snap.Messages != 1 || snap.TranscriptTokens <= 0 {
		t.Fatalf("transcript fields = %d messages / %d tokens", snap.Messages, snap.TranscriptTokens)
	}
	if snap.BudgetSoft <= 0 || snap.BudgetHard <= snap.BudgetSoft {
		t.Fatalf("budgets = %v / %v", snap.BudgetSoft, snap.BudgetHard)
	}
	sum := 0
	for _, s := range snap.Breakdown {
		sum += s.Tokens
	}
	// Two prompt sections plus the round's output segment, summing to the
	// reported total.
	if len(snap.Breakdown) != 3 || sum != 1050 {
		t.Fatalf("breakdown should rescale to the reported total 1050, got %+v", snap.Breakdown)
	}

	// Advertised to peers (not Internal), so the control plane can ask too —
	// but absent from the reason family's own tool scope, so the model never
	// sees it as one of its tools.
	var advertised bool
	for _, entry := range w.WatchEntries(false) {
		if entry["event"] == string(TypeContextStatus) {
			advertised = true
		}
	}
	if !advertised {
		t.Fatal("context.status should be in the peer-facing ready contract")
	}
	for _, def := range w.LLMToolDefs() {
		if def.Name == string(TypeContextStatus) {
			t.Fatalf("context.status must not be one of the model's tools: %+v", def)
		}
	}
}

// TestContextStatusBeforeFirstRound verifies the query still answers before any
// round completed: window, transcript size and budgets, with rounds=0 and no
// usage fields invented.
func TestContextStatusBeforeFirstRound(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(&staticProvider{}, ch)
	w.mu.Lock()
	w.contextWindow = 8000
	w.mu.Unlock()

	w.process(context.Background(), event.New(TypeContextStatus, "caller", nil))

	replies := ch.eventsOf(event.TypeRequestCompleted)
	if len(replies) != 1 {
		t.Fatalf("expected one reply, got %d", len(replies))
	}
	payload := replies[0].Payload
	if payload["context_window"] != 8000 {
		t.Fatalf("context_window = %v", payload["context_window"])
	}
	if payload["rounds"] != uint64(0) {
		t.Fatalf("rounds = %v, want 0", payload["rounds"])
	}
	if _, ok := payload["input_tokens"]; ok {
		t.Fatalf("no round yet, so no usage should be reported: %+v", payload)
	}
}

// TestContextUsageSurvivesSnapshot verifies the accounting is part of the
// worker state: it comes back after a restore, and the round counter keeps
// counting from where it left off.
func TestContextUsageSurvivesSnapshot(t *testing.T) {
	cached := 900
	w1 := newTestWorker(&staticProvider{}, newTestChannel())
	w1.mu.Lock()
	w1.contextWindow = 1000
	w1.roundBreakdown = []contextSection{{Key: breakdownSystem, Tokens: 1000}}
	w1.recordContextUsage(llm.Message{Usage: &llm.Usage{
		InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050, CacheReadTokens: &cached,
	}})
	w1.mu.Unlock()

	snap, err := w1.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	w2 := newTestWorker(&staticProvider{}, newTestChannel())
	if err := w2.Restore(snap); err != nil {
		t.Fatal(err)
	}
	w2.mu.Lock()
	cu := w2.contextUsage
	w2.mu.Unlock()
	if cu == nil {
		t.Fatal("context usage did not survive the snapshot")
	}
	if cu.InputTokens != 1000 || cu.OutputTokens != 50 || cu.Rounds != 1 || cu.At == 0 {
		t.Fatalf("restored usage = %+v", cu)
	}
	if cu.CacheReadTokens == nil || *cu.CacheReadTokens != 900 {
		t.Fatalf("restored cache read = %+v", cu.CacheReadTokens)
	}
	if len(cu.Breakdown) != 1 || cu.Breakdown[0].Key != breakdownSystem {
		t.Fatalf("restored breakdown = %+v", cu.Breakdown)
	}

	// The counter continues across the restore rather than restarting at 1.
	w2.mu.Lock()
	w2.recordContextUsage(llm.Message{Usage: &llm.Usage{InputTokens: 10, OutputTokens: 1, TotalTokens: 11}})
	rounds := w2.contextUsage.Rounds
	w2.mu.Unlock()
	if rounds != 2 {
		t.Fatalf("rounds after restore = %d, want 2", rounds)
	}
}
