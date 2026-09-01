package reason

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	reasonBase "github.com/niq-run/niq/pkg/reason"
)

// summarizeProvider routes Complete (summarizer) to a fixed digest, recording
// the summarizer's system prompt so tests can assert on update-mode behavior.
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

// newReasonBase builds a bare BaseReasonWorker wired like NewWorker (keepTail
// flows through Config) but without starting the watch loop — enough for
// exercising the context meta ops synchronously from a test. The context edit
// is driven by handleContextOp, which edits the transcript directly.
func newReasonBase(t *testing.T, prov llm.LLMProvider, keepTail int, seed []llm.Message) *reasonBase.BaseReasonWorker {
	t.Helper()
	w := reasonBase.NewBaseReasonWorker(reasonBase.Config{
		ID: "r1", Provider: prov, Bus: newMockChannel(),
		ContextWindow: 1000, KeepTail: keepTail, SeedMessages: seed,
	})
	return w
}

// TestProjectTranscriptStrips verifies the projection drops thinking blocks
// and truncates oversized tool results.
func TestProjectTranscriptStrips(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.ContentText, Text: "hello"},
			{Type: llm.ContentImage, Data: strings.Repeat("x", 100000)},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.ContentThinking, Text: "long reasoning that must not leak"},
		}},
		{Role: llm.RoleToolResult, ToolName: "bash", Content: []llm.ContentBlock{
			{Type: llm.ContentText, Text: strings.Repeat("y", 5000)},
		}},
	}
	out := projectTranscript(msgs)
	if strings.Contains(out, "long reasoning") {
		t.Fatal("thinking must be stripped from projection")
	}
	if strings.Contains(out, "xxxx") {
		t.Fatal("image data must not appear in projection")
	}
	if !strings.Contains(out, "[truncated]") {
		t.Fatal("oversized tool result must be truncated")
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "omitted") {
		t.Fatal("text content and image omission marker expected")
	}
}

// TestCurrentDigestNegative verifies currentDigest returns empty for a
// transcript whose head is not a digest message.
func TestCurrentDigestNegative(t *testing.T) {
	if d := currentDigest([]llm.Message{{Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hello"}}}}); d != "" {
		t.Fatalf("expected empty digest, got %q", d)
	}
}

// TestCompactionUpdateMode verifies a second compaction detects the carried
// digest and sends it to the summarizer for incremental merging.
func TestCompactionUpdateMode(t *testing.T) {
	prov := &summarizeProvider{summarized: "v2",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	seed := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: "[context digest] v1: goal=fix bug; done=analysis"}}},
	}
	for i := 0; i < 4; i++ {
		seed = append(seed, llm.Message{Role: llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "step"}}})
	}
	w := newReasonBase(t, prov, 1, seed)

	handleContextOp(w, event.New(event.TypeWorkerUpdate, "me", map[string]any{"op": "context.compress"}), "")

	waitCond(t, 2*time.Second, func() bool {
		return strings.Contains(prov.prompt(), "v1: goal=fix bug")
	}, "summarizer to receive previous digest")

	prompt := prov.prompt()
	if !strings.Contains(prompt, "v1: goal=fix bug") {
		t.Fatalf("update mode must pass previous digest to summarizer, prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "update it incrementally") {
		t.Fatalf("update-mode directive missing, prompt: %q", prompt)
	}
	// New head carries the v2 digest.
	waitCond(t, 2*time.Second, func() bool {
		msgs := w.Messages()
		return len(msgs) > 0 && strings.Contains(msgs[0].Content[0].Text, "DIGEST(v2)")
	}, "updated digest head to be applied")
}

// firstText returns the text of the first content block of m, or "" if the
// message has no content blocks.
func firstText(m llm.Message) string {
	if len(m.Content) == 0 {
		return ""
	}
	return m.Content[0].Text
}

// TestCompactionAppendsNote verifies a successful compaction appends a
// completion note to the transcript, so the model knows the operation ran and
// does not re-decide to compress every round.
func TestCompactionAppendsNote(t *testing.T) {
	prov := &summarizeProvider{summarized: "s1",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	seed := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "step1"}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "step2"}}},
	}
	w := newReasonBase(t, prov, 1, seed)

	handleContextOp(w, event.New(event.TypeWorkerUpdate, "me", map[string]any{"op": "context.compress"}), "")

	waitCond(t, 2*time.Second, func() bool {
		msgs := w.Messages()
		for _, m := range msgs {
			if strings.Contains(firstText(m), "context compressed") {
				return true
			}
		}
		return false
	}, "compaction note to be appended")
}

// TestMetaCompressViaUpdateRequested verifies the compress meta operation,
// run via handleContextOp, compacts the transcript asynchronously and
// schedules the next round.
func TestMetaCompressViaUpdateRequested(t *testing.T) {
	prov := &summarizeProvider{summarized: "hard-budget",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	seed := make([]llm.Message, 0, 6)
	for i := 0; i < 6; i++ {
		seed = append(seed, llm.Message{Role: llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: string(rune('a' + i))}}})
	}
	w := newReasonBase(t, prov, 2, seed)

	handleContextOp(w, event.New(event.TypeWorkerUpdate, "me", map[string]any{"op": "context.compress"}), "")

	waitCond(t, 2*time.Second, func() bool {
		msgs := w.Messages()
		for _, m := range msgs {
			if len(m.Content) > 0 && strings.Contains(m.Content[0].Text, "DIGEST") {
				return true
			}
		}
		return false
	}, "compress to apply a digest head")
}

// TestMetaRotateViaUpdateRequested verifies the rotate meta operation runs
// asynchronously: it compacts the transcript into a carried digest head.
func TestMetaRotateViaUpdateRequested(t *testing.T) {
	prov := &summarizeProvider{summarized: "episode",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	seed := make([]llm.Message, 0, 6)
	for i := 0; i < 4; i++ {
		seed = append(seed, llm.Message{Role: llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: string(rune('a' + i))}}})
	}
	seed = append(seed, llm.Message{Role: llm.RoleAssistant, StopReason: "tool_calls",
		Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolCallID: "call_ep", ToolName: "context_rotate"}}})
	seed = append(seed, llm.Message{Role: llm.RoleToolResult, ToolCallID: "call_ep", ToolName: "context_rotate",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "[pending]"}}})
	w := newReasonBase(t, prov, 2, seed)

	handleContextOp(w, event.New(event.TypeWorkerUpdate, "me", map[string]any{"op": "context.rotate"}), "")

	waitCond(t, 2*time.Second, func() bool {
		msgs := w.Messages()
		return len(msgs) > 0 && strings.Contains(msgs[0].Content[0].Text, "DIGEST")
	}, "rotate to complete")
}
