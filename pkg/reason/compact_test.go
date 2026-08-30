package reason

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
)

// summarizeProvider routes Complete (summarizer) and CompleteStream (reasoning)
// to different fixed messages, recording the summarizer's system prompt.
type summarizeProvider struct {
	mu          sync.Mutex
	seenPrompt  string
	summarized  string
	chatMessage llm.Message
}

func (p *summarizeProvider) Complete(_ context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seenPrompt = req.Context.SystemPrompt
	return &llm.CompletionResponse{Message: llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "DIGEST(" + p.summarized + ")"}},
	}}, nil
}

func (p *summarizeProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextEnd{})
	es.End(p.chatMessage)
	return es, nil
}

func (p *summarizeProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func (p *summarizeProvider) prompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seenPrompt
}

// TestSoftBudgetInjectsReminder verifies crossing the soft threshold appends
// exactly one system reminder per crossing, and no compaction runs.
func TestSoftBudgetInjectsReminder(t *testing.T) {
	prov := &summarizeProvider{summarized: "unused",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
			Usage:   &llm.Usage{InputTokens: 900, OutputTokens: 10},
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}}}
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: prov, Bus: newTestChannel(),
		ContextWindow: 1000, BudgetSoft: 0.85, BudgetHard: 0.97})

	w.handleBudget(context.Background(), llm.Message{Usage: &llm.Usage{InputTokens: 900, OutputTokens: 10}})

	msgs := w.transcript.Render()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content[0].Text, "91%") {
		t.Fatalf("expected one budget reminder, got %+v", msgs)
	}

	// Same side of the threshold: no duplicate reminder.
	w.handleBudget(context.Background(), llm.Message{Usage: &llm.Usage{InputTokens: 950, OutputTokens: 5}})
	if got := len(w.transcript.Render()); got != 1 {
		t.Fatalf("reminder should fire once per crossing, got %d messages", got)
	}
}

// TestHardBudgetCompacts verifies crossing the hard threshold compacts the
// transcript asynchronously: digest head + keepTail tail.
func TestHardBudgetEmitsMetaRequest(t *testing.T) {
	prov := &summarizeProvider{summarized: "hard-budget",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	ch := newTestChannel()
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: prov, Bus: ch,
		ContextWindow: 1000, BudgetSoft: 0.85, BudgetHard: 0.97, KeepTail: 2})

	seed := func(n int) {
		for i := 0; i < n; i++ {
			w.transcript.Apply(InputPatch{Messages: []llm.Message{
				{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: string(rune('a' + i))}}},
			}})
		}
	}
	seed(6)

	w.handleBudget(context.Background(), llm.Message{Usage: &llm.Usage{InputTokens: 990, OutputTokens: 5}})

	// Hard budget routes compaction through a worker.update meta
	// request to itself (single audit path), not a direct compactor call.
	var found bool
	for _, e := range ch.eventsOf(event.TypeWorkerUpdate) {
		if op, _ := e.Payload["op"].(string); op == "context.compress" {
			found = true
		}
	}
	if !found {
		t.Fatalf("hard budget should emit a worker.update compress request, got %+v", ch.eventsOf(event.TypeWorkerUpdate))
	}
}

// TestProjectTranscriptStrips and the context-compaction tests moved to
// pkg/worker/reason, where the default worker's context strategy (a package
// function, not a reason-package interface) lives and rewrites the transcript
// directly in response to the context.compress convention event.
