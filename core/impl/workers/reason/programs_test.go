package reason

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	reasonBase "github.com/niq-run/niq/core/impl/reason"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/llm"
	"github.com/niq-run/niq/core/itfs/program"
)

const progTestTimeout = 2 * time.Second

// selfLoopChannel wraps mockChannel to model the engine's directed-delivery
// semantics: a Send addressed to the worker itself lands in its watch mailbox
// (production routes it there; plain mockChannel only records). Broadcasts
// stay record-only, matching what the assertions rely on.
type selfLoopChannel struct {
	*mockChannel
}

func (c *selfLoopChannel) Send(_ context.Context, evt event.Event, targets ...string) error {
	if err := c.mockChannel.Send(context.Background(), evt, targets...); err != nil {
		return err
	}
	for _, t := range targets {
		if t == "r1" {
			c.in <- evt
		}
	}
	return nil
}

// TestProgramQueryViaBus drives program.query through the real dispatch path
// (watch loop → process → DispatchExtension → handler). This is the path the
// direct method tests miss: process holds w.mu for the whole dispatch, so a
// handler that takes w.mu itself self-deadlocks the event loop — exactly the
// bug this guards against (the worker would hang and no reply would appear).
func TestProgramQueryViaBus(t *testing.T) {
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: ch})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	ch.in <- event.New(TypeProgramQuery, "tester", map[string]any{})
	waitCond(t, progTestTimeout, func() bool {
		return len(ch.eventsOf(event.TypeRequestCompleted)) > 0
	}, "request.completed reply for program.query")

	completed := ch.eventsOf(event.TypeRequestCompleted)[0]
	res := event.ReplyText(completed.Payload)
	var programs []program.Program
	if err := json.Unmarshal([]byte(res), &programs); err != nil {
		t.Fatalf("query result is not valid JSON: %v (result=%q)", err, res)
	}
	if len(programs) != 0 {
		t.Fatalf("expected empty program list, got %+v", programs)
	}
}

// TestProgramAttachDetachViaBus mounts and unmounts a program through the real
// dispatch path and verifies the durable-change signal fires and a follow-up
// query sees the change.
func TestProgramAttachDetachViaBus(t *testing.T) {
	var calls int32c
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: ch,
		OnDurableChange: func() { calls.add() }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	ch.in <- event.New(TypeProgramAttach, "tester", map[string]any{
		"name":         "p1",
		"content_type": "playbook",
		"description":  "test playbook",
	})
	waitCond(t, progTestTimeout, func() bool {
		for _, e := range ch.eventsOf(event.TypeRequestCompleted) {
			if event.ReplyText(e.Payload) == "attached program p1" {
				return true
			}
		}
		return false
	}, "request.completed for program.attach")
	waitCond(t, progTestTimeout, func() bool { return calls.get() > 0 }, "durable-change callback")

	// Query again: the added program must be visible.
	ch.in <- event.New(TypeProgramQuery, "tester", map[string]any{})
	waitCond(t, progTestTimeout, func() bool {
		return len(ch.eventsOf(event.TypeRequestCompleted)) >= 2
	}, "request.completed reply for second program.query")
	completed := ch.eventsOf(event.TypeRequestCompleted)[1]
	res := event.ReplyText(completed.Payload)
	var programs []program.Program
	if err := json.Unmarshal([]byte(res), &programs); err != nil {
		t.Fatalf("query result is not valid JSON: %v (result=%q)", err, res)
	}
	if len(programs) != 1 || programs[0].Name != "p1" {
		t.Fatalf("query after add: programs=%+v", programs)
	}
	// The path travels with the entry so callers can load its content from
	// the program worker.
	if programs[0].Path != "p1" {
		t.Fatalf("query after add: Path = %q, want %q", programs[0].Path, "p1")
	}

	// Detaching the same program succeeds and is visible.
	ch.in <- event.New(TypeProgramDetach, "tester", map[string]any{"name": "p1"})
	waitCond(t, progTestTimeout, func() bool {
		for _, e := range ch.eventsOf(event.TypeRequestCompleted) {
			if r, _ := e.Payload["result"].(string); r == "detached program p1" {
				return true
			}
		}
		return false
	}, "request.completed for program.detach")
}

// TestProgramFromArgsPinsPathAndPlaybookReference covers the two rules that
// make a query result usable: every program carries Path == its name (so
// program__read has an address to take), and a playbook stores no content
// (it is only a reference into the program worker).
func TestProgramFromArgsPinsPathAndPlaybookReference(t *testing.T) {
	instr, err := programFromArgs(map[string]any{
		"name": "rules", "content_type": "instruction", "content": "do this",
	})
	if err != nil {
		t.Fatalf("instruction: %v", err)
	}
	if instr.Path != "rules" || instr.EntryContent.Path != "rules" {
		t.Fatalf("instruction Path = %q / entry %q, want rules", instr.Path, instr.EntryContent.Path)
	}
	if instr.EntryContent.Content != "do this" {
		t.Fatalf("instruction content = %q, want it kept", instr.EntryContent.Content)
	}

	pb, err := programFromArgs(map[string]any{
		"name": "deploy", "content_type": "playbook", "content": "should be dropped",
	})
	if err != nil {
		t.Fatalf("playbook: %v", err)
	}
	if pb.Path != "deploy" {
		t.Fatalf("playbook Path = %q, want deploy", pb.Path)
	}
	if pb.EntryContent.Content != "" {
		t.Fatalf("playbook content = %q, want empty (reference only)", pb.EntryContent.Content)
	}

	if _, err := programFromArgs(map[string]any{
		"name": "bad", "content_type": "instruction",
	}); err == nil {
		t.Fatal("instruction without content: want error")
	}
}

// int32c is a tiny atomic counter for the durable-change hook.
type int32c struct {
	v atomic.Int32
}

func (c *int32c) add() { c.v.Add(1) }
func (c *int32c) get() int32 {
	return c.v.Load()
}

// scriptedProvider plays a fixed sequence of assistant messages: round 1 attaches
// a program via program_attach, round 2 lists them via program_query, round 3
// answers with text — the exact add→query flow, driven through real reasoning
// rounds so tool results land in the transcript.
type scriptedProvider struct {
	mu     sync.Mutex
	rounds int
}

func (p *scriptedProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, nil
}

func (p *scriptedProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	p.mu.Lock()
	p.rounds++
	n := p.rounds
	p.mu.Unlock()
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextEnd{})
	switch n {
	case 1:
		es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "tool_calls",
			Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolName: "program_attach",
				ToolCallID:    "call_add",
				ToolArguments: `{"name":"p1","content_type":"playbook","description":"d"}`}}})
	case 2:
		es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "tool_calls",
			Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolName: "program_query",
				ToolCallID: "call_query", ToolArguments: `{}`}}})
	default:
		es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "done"}}})
	}
	return es, nil
}

func (p *scriptedProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

// TestProgramToolCallAdvancesRound is the end-to-end regression for the reply
// path: the update's tool result must resolve and start the next round, and
// the query's serialized list must reach the transcript as the tool's text —
// resultOutcome reads the reply's "result" field when present, so a reply that
// carries its data under any other key would otherwise leave the LLM an EMPTY
// tool result (it would conclude the list is empty even though the query
// succeeded).
func TestProgramToolCallAdvancesRound(t *testing.T) {
	prov := &scriptedProvider{}
	ch := &selfLoopChannel{mockChannel: newMockChannel()}
	w := NewWorker(Config{ID: "r1", Provider: prov, Bus: ch})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	// The self-ready announcement is queued first: tool dispatch resolves
	// capabilities through the discovery universe (HandleWorkerReady), which
	// in production is fed by the self-ready round-trip. The mock channel's
	// Send only records — it does not loop back into the watch loop — so the
	// test replays that announcement explicitly. FIFO ordering guarantees it
	// is processed before the input.
	ch.in <- event.New(event.TypeWorkerReady, "r1", map[string]any{
		"worker_id": "r1",
		"type":      "reason",
		"watch":     w.WatchEntries(true),
	})
	ch.in <- event.New(event.TypeWorkerInput, "tester", map[string]any{"text": "add then list your programs"})

	// Round 3 runs ("done") only if both tool results resolved.
	waitCond(t, progTestTimeout, func() bool {
		for _, e := range ch.eventsOf("reason.response") {
			if texts, ok := e.Payload["content"].([]any); ok && len(texts) > 0 {
				if s, _ := texts[0].(string); s == "done" {
					return true
				}
			}
		}
		return false
	}, "final reasoning round after update and query tool results")

	// The query's serialized list reached the transcript as tool-result text.
	waitCond(t, progTestTimeout, func() bool {
		for _, msg := range w.Messages() {
			for _, block := range msg.Content {
				if block.Type == llm.ContentText && strings.Contains(block.Text, `"name":"p1"`) {
					return true
				}
			}
		}
		return false
	}, "query result JSON in the transcript")
}

// recordingProvider answers every reasoning round with plain text and records
// the system prompt each request carried, so a test can assert that the head of
// the request stayed byte-identical across a program change.
type recordingProvider struct {
	mu      sync.Mutex
	prompts []string
}

func (p *recordingProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, nil
}

func (p *recordingProvider) CompleteStream(_ context.Context, req *llm.CompletionRequest) (*llm.EventStream, error) {
	p.mu.Lock()
	p.prompts = append(p.prompts, req.Context.SystemPrompt)
	p.mu.Unlock()
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextDelta{Delta: "ok"})
	es.Push(llm.EventTextEnd{})
	es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}})
	return es, nil
}

func (p *recordingProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func (p *recordingProvider) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}

// TestProgramChangeStaysOutOfPromptAfterFirstRound is the end-to-end contract of
// the prompt-cache optimization: once a request has gone out, an attach is
// recorded as a [system] note (carrying the instruction's text, so the rule is
// in force immediately) while every request's system prompt keeps its original
// bytes. Only a compaction folds the change in.
func TestProgramChangeStaysOutOfPromptAfterFirstRound(t *testing.T) {
	prov := &recordingProvider{}
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: prov, Bus: ch})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	// One round first: this is what puts a prompt into the provider's cache.
	ch.in <- event.New(event.TypeWorkerInput, "tester", map[string]any{"text": "begin"})
	waitCond(t, progTestTimeout, func() bool { return len(prov.recorded()) >= 1 }, "first reasoning round")

	ch.in <- event.New(TypeProgramAttach, "tester", map[string]any{
		"name": "rules", "content_type": "instruction", "content": "ALWAYS-X",
	})
	waitCond(t, progTestTimeout, func() bool {
		for _, e := range ch.eventsOf(event.TypeRequestCompleted) {
			if event.ReplyText(e.Payload) == "attached program rules" {
				return true
			}
		}
		return false
	}, "request.completed for program.attach")

	// The change is in force immediately: the note carries the rule's text.
	waitCond(t, progTestTimeout, func() bool {
		for _, msg := range w.Messages() {
			for _, block := range msg.Content {
				if block.Type == llm.ContentText &&
					strings.Contains(block.Text, "[system] A new instruction program") &&
					strings.Contains(block.Text, "ALWAYS-X") {
					return true
				}
			}
		}
		return false
	}, "[system] note describing the attach")

	// Another round must still not put it in the prompt (no reset boundary yet).
	ch.in <- event.New(event.TypeWorkerInput, "tester", map[string]any{"text": "keep going"})
	waitCond(t, progTestTimeout, func() bool { return len(prov.recorded()) >= 2 }, "second reasoning round")
	for i, prompt := range prov.recorded() {
		if strings.Contains(prompt, "ALWAYS-X") {
			t.Fatalf("round %d put the pending program into the system prompt; it belongs in the note until a compaction:\n%s", i, prompt)
		}
	}
}

// TestProgramOpNoteWording pins the note's contract: an attached instruction
// travels with its full text (it is in force only through this note until the
// prompt is re-rendered), a playbook travels as a reference, and an oversized
// instruction degrades to a pointer instead of a rule cut off mid-character.
func TestProgramOpNoteWording(t *testing.T) {
	instr := program.Program{
		Meta:         program.Meta{Name: "rules", ContentType: program.ContentTypeInstruction},
		Path:         "rules",
		EntryContent: program.ProgramContent{Content: "ALWAYS do X"},
	}
	n := programOpNote(reasonBase.ProgramOp{Kind: reasonBase.ProgramOpAttach, Program: &instr, Name: "rules", ContentType: program.ContentTypeInstruction})
	if !strings.Contains(n, "in force") || !strings.Contains(n, "ALWAYS do X") {
		t.Fatalf("instruction note must state the rule and its text, got: %q", n)
	}

	book := program.Program{
		Meta: program.Meta{Name: "deploy", ContentType: program.ContentTypePlaybook, Description: "how to deploy", Tags: []string{"ops"}},
		Path: "deploy",
	}
	n = programOpNote(reasonBase.ProgramOp{Kind: reasonBase.ProgramOpAttach, Program: &book, Name: "deploy", ContentType: program.ContentTypePlaybook})
	if !strings.Contains(n, "how to deploy") || !strings.Contains(n, `program__read("deploy")`) {
		t.Fatalf("playbook note must be a usable reference, got: %q", n)
	}

	n = programOpNote(reasonBase.ProgramOp{Kind: reasonBase.ProgramOpDetach, Name: "rules", ContentType: program.ContentTypeInstruction})
	if !strings.Contains(n, "no longer in force") {
		t.Fatalf("instruction detach must revoke the rule, got: %q", n)
	}

	// Oversized instruction: multibyte text must not be cut mid-rune, and the
	// note points at the authoritative copy instead.
	huge := instr
	huge.EntryContent.Content = strings.Repeat("規", programNoteMaxBytes)
	n = programOpNote(reasonBase.ProgramOp{Kind: reasonBase.ProgramOpAttach, Program: &huge, Name: "rules", ContentType: program.ContentTypeInstruction})
	if !utf8.ValidString(n) {
		t.Fatalf("note must stay valid UTF-8 after truncation: %q", n[len(n)-40:])
	}
	if !strings.Contains(n, `program__read("rules")`) {
		t.Fatalf("truncated instruction must hand over the path, got: %q", n)
	}
}

// TestProgramChangeBeforeFirstRoundIsNotNoted: with no request sent yet there is
// no cached prefix to protect, so the change is rendered directly and no note is
// appended (it would only duplicate the prompt).
func TestProgramChangeBeforeFirstRoundIsNotNoted(t *testing.T) {
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: ch})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	ch.in <- event.New(TypeProgramAttach, "tester", map[string]any{
		"name": "rules", "content_type": "instruction", "content": "ALWAYS-X",
	})
	waitCond(t, progTestTimeout, func() bool {
		for _, e := range ch.eventsOf(event.TypeRequestCompleted) {
			if event.ReplyText(e.Payload) == "attached program rules" {
				return true
			}
		}
		return false
	}, "request.completed for program.attach")

	for _, msg := range w.Messages() {
		for _, block := range msg.Content {
			if block.Type == llm.ContentText && strings.Contains(block.Text, "[system] A new instruction program") {
				t.Fatalf("no note should be appended before the first request: %q", block.Text)
			}
		}
	}
}

// reasoningPromptProvider records the system prompt of every reasoning request
// it serves. Summarization requests are recognized by the digest directive and
// excluded, so the recording is exactly the worker's own request head.
type reasoningPromptProvider struct {
	mu      sync.Mutex
	prompts []string
}

func (p *reasoningPromptProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}}}, nil
}

func (p *reasoningPromptProvider) CompleteStream(_ context.Context, req *llm.CompletionRequest) (*llm.EventStream, error) {
	summarizing := req.Context != nil && strings.Contains(req.Context.SystemPrompt, "Summarize the following reasoning transcript")
	p.mu.Lock()
	if !summarizing {
		p.prompts = append(p.prompts, req.Context.SystemPrompt)
	}
	p.mu.Unlock()

	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextDelta{Delta: "ok"})
	es.Push(llm.EventTextEnd{})
	es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}})
	return es, nil
}

func (p *reasoningPromptProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

func (p *reasoningPromptProvider) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}

// TestCompactionFoldsPendingProgramIntoPrompt closes the loop: a program attached
// while a prompt was cached stays out of it, and the compaction that follows is
// the boundary at which the next round renders it — the ledger is settled exactly
// once, at the moment a cache reset is already paid for.
func TestCompactionFoldsPendingProgramIntoPrompt(t *testing.T) {
	prov := &reasoningPromptProvider{}
	ch := newMockChannel()
	// TailTokens=1 makes the compaction fold the history away, i.e. a real
	// rewrite — the reset boundary the ledger waits for.
	w := NewWorker(Config{ID: "r1", Provider: prov, Bus: ch, TailTokens: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	// A first round puts a prompt into the provider's cache.
	ch.in <- event.New(event.TypeWorkerInput, "tester", map[string]any{"text": "begin"})
	waitCond(t, progTestTimeout, func() bool { return len(prov.recorded()) >= 1 }, "first reasoning round")

	ch.in <- event.New(TypeProgramAttach, "tester", map[string]any{
		"name": "rules", "content_type": "instruction", "content": "ALWAYS-X",
	})
	waitCond(t, progTestTimeout, func() bool {
		for _, e := range ch.eventsOf(event.TypeRequestCompleted) {
			if event.ReplyText(e.Payload) == "attached program rules" {
				return true
			}
		}
		return false
	}, "request.completed for program.attach")

	// The auto-fired compression path (no tool call id) rewrites the transcript
	// and schedules the round that settles the ledger.
	ch.in <- event.New(reasonBase.TypeContextCompress, "tester", nil)
	waitCond(t, progTestTimeout, func() bool {
		prompts := prov.recorded()
		return len(prompts) >= 2 && strings.Contains(prompts[len(prompts)-1], "ALWAYS-X")
	}, "post-compaction round rendering the pending program")
}
