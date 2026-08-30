package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/llm"
)

func TestProviderCompleteToolCall(t *testing.T) {
	var gotReq messagesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(messagesResponse{
			ID: "msg_1",
			Content: []respContentBlock{
				{Type: "text", Text: "Hello"},
				{Type: "tool_use", ID: "toolu_1", Name: "workspace.read_file", Input: json.RawMessage(`{"path":"a.go"}`)},
			},
			StopReason: stringPtr("tool_use"),
			Usage: &anthropicUsage{
				InputTokens:          3,
				OutputTokens:         4,
				CacheReadInputTokens: 2,
			},
		})
	}))
	defer srv.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "claude-test",
		Client:  srv.Client(),
	})

	resp, err := p.Complete(context.Background(), &llm.CompletionRequest{
		Context: &llm.Context{
			SystemPrompt: "sys",
			Messages: []llm.Message{{
				Role:    llm.RoleUser,
				Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "read it"}},
			}},
			Tools: []llm.ToolDef{{Name: "workspace.read_file", Description: "read", Parameters: map[string]any{"type": "object"}}},
		},
		ToolChoice: llm.ToolChoiceRequired,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotReq.Model != "claude-test" || gotReq.System != "sys" || gotReq.Stream {
		t.Fatalf("unexpected request: %+v", gotReq)
	}
	if len(gotReq.Tools) != 1 || gotReq.Tools[0].Name != "workspace.read_file" {
		t.Fatalf("unexpected tools: %+v", gotReq.Tools)
	}
	tc, ok := gotReq.ToolChoice.(map[string]any)
	if !ok || tc["type"] != "any" {
		t.Fatalf("tool choice = %#v", gotReq.ToolChoice)
	}

	if len(resp.Message.Content) != 2 {
		t.Fatalf("content blocks = %d", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Type != llm.ContentText || resp.Message.Content[0].Text != "Hello" {
		t.Fatalf("text block = %+v", resp.Message.Content[0])
	}
	if resp.Message.Content[1].Type != llm.ContentToolCall ||
		resp.Message.Content[1].ToolCallID != "toolu_1" ||
		resp.Message.Content[1].ToolName != "workspace.read_file" ||
		resp.Message.Content[1].ToolArguments != `{"path":"a.go"}` {
		t.Fatalf("tool block = %+v", resp.Message.Content[1])
	}
	if resp.Usage.InputTokens != 3 || resp.Usage.OutputTokens != 4 || resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 2 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestProviderStreamText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/v1/messages") {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`
event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}
`))
	}))
	defer srv.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "claude-stream",
		Client:  srv.Client(),
	})

	es, err := p.CompleteStream(context.Background(), &llm.CompletionRequest{
		Context: &llm.Context{
			Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}}}},
		},
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	msg, err := es.Result(context.Background())
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(msg.Content) != 1 || msg.Content[0].Type != llm.ContentText || msg.Content[0].Text != "Hi" {
		t.Fatalf("message = %+v", msg.Content)
	}
	if msg.Usage == nil || msg.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", msg.Usage)
	}
}

func stringPtr(s string) *string {
	return &s
}
