package openairesponses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/llm"
)

func TestProviderCompleteFunctionCall(t *testing.T) {
	var gotReq responsesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responsesResponse{
			ID:     "resp_1",
			Status: "completed",
			Output: []responseOutputItem{
				{
					Type: "message",
					Role: "assistant",
					Content: []responseOutputContent{
						{Type: "output_text", Text: "Hello"},
					},
				},
				{
					Type:      "function_call",
					ID:        "fc_1",
					CallID:    "call_1",
					Name:      "workspace.read_file",
					Arguments: json.RawMessage(`{"path":"a.go"}`),
				},
			},
			Usage: &responsesUsage{
				InputTokens:  5,
				OutputTokens: 6,
				TotalTokens:  11,
				InputDetails: &responsesUsageDetail{CachedTokens: 2},
			},
		})
	}))
	defer srv.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "responses-test",
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

	if gotReq.Model != "responses-test" || gotReq.Instructions != "sys" || gotReq.Stream {
		t.Fatalf("unexpected request: %+v", gotReq)
	}
	if len(gotReq.Tools) != 1 || gotReq.Tools[0].Name != "workspace.read_file" {
		t.Fatalf("unexpected tools: %+v", gotReq.Tools)
	}
	if gotReq.ToolChoice != "required" {
		t.Fatalf("tool choice = %#v", gotReq.ToolChoice)
	}

	if len(resp.Message.Content) != 2 {
		t.Fatalf("content blocks = %d", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Type != llm.ContentText || resp.Message.Content[0].Text != "Hello" {
		t.Fatalf("text block = %+v", resp.Message.Content[0])
	}
	if resp.Message.Content[1].Type != llm.ContentToolCall ||
		resp.Message.Content[1].ToolCallID != "call_1" ||
		resp.Message.Content[1].ToolName != "workspace.read_file" ||
		resp.Message.Content[1].ToolArguments != `{"path":"a.go"}` {
		t.Fatalf("tool block = %+v", resp.Message.Content[1])
	}
	if resp.Usage.InputTokens != 5 || resp.Usage.OutputTokens != 6 || resp.Usage.TotalTokens != 11 || resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 2 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestProviderStreamTextAndFunctionCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/responses") {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`
event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.output_text.done
data: {"type":"response.output_text.done"}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item_id":"fc_1","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"workspace.read_file"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{\"path\":\"a.go\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_1","arguments":"{\"path\":\"a.go\"}"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":5,"output_tokens":6,"total_tokens":11}}}
`))
	}))
	defer srv.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "responses-stream",
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
	if len(msg.Content) != 2 {
		t.Fatalf("content blocks = %d", len(msg.Content))
	}
	if msg.Content[0].Type != llm.ContentText || msg.Content[0].Text != "Hello" {
		t.Fatalf("text block = %+v", msg.Content[0])
	}
	if msg.Content[1].Type != llm.ContentToolCall ||
		msg.Content[1].ToolCallID != "call_1" ||
		msg.Content[1].ToolName != "workspace.read_file" ||
		msg.Content[1].ToolArguments != `{"path":"a.go"}` {
		t.Fatalf("tool block = %+v", msg.Content[1])
	}
	if msg.Usage == nil || msg.Usage.TotalTokens != 11 {
		t.Fatalf("usage = %+v", msg.Usage)
	}
}
