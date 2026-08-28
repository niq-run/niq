package openai

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/54c1/niq/core/llm"
)

// TestReadStreamSynthesizesMissingToolCallID verifies that a model which omits
// the tool-call id (e.g. THUDM/GLM) still produces a valid ToolCallID, so the
// downstream OpenAI-format request (call + tool_result) round-trips without a
// "missing messages.tool_call_id" error.
func TestReadStreamSynthesizesMissingToolCallID(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant"},"finish_reason":null}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]},"finish_reason":null}]}`,
		``,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	es := llm.NewEventStream()
	p := New(Config{})
	p.readStream(io.NopCloser(strings.NewReader(body)), es)
	msg, err := es.Result(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got string
	for _, b := range msg.Content {
		if b.Type == llm.ContentToolCall {
			got = b.ToolCallID
		}
	}
	if got != "call_0" {
		t.Fatalf("expected synthesized ToolCallID call_0, got %q", got)
	}
}

// TestToCompletionResponseSynthesizesMissingToolCallID covers the non-streaming
// path, which has the same omission.
func TestToCompletionResponseSynthesizesMissingToolCallID(t *testing.T) {
	fr := "tool_calls"
	cr := &chatCompletionResponse{
		Choices: []choice{{
			Message: chatRespMsg{
				Role: "assistant",
				ToolCalls: []chatToolCall{{
					Function: chatFnCall{Name: "bash", Arguments: "{}"},
				}},
			},
			FinishReason: &fr,
		}},
	}
	resp := New(Config{}).toCompletionResponse(cr)
	msg := resp.Message
	var got string
	for _, b := range msg.Content {
		if b.Type == llm.ContentToolCall {
			got = b.ToolCallID
		}
	}
	if got != "call_0" {
		t.Fatalf("expected synthesized ToolCallID call_0, got %q", got)
	}
}
