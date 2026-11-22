package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// StubModel is an OpenAI-compatible chat-completions endpoint that answers with
// a fixed text. It is how the reason path is exercised without a model: the
// project's provider.json points at this server, so the adapter, the transcript
// and the reply events are all real.
type StubModel struct {
	srv  *httptest.Server
	text string

	mu       sync.Mutex
	requests []map[string]any
}

// NewStubModel starts the server and stops it when the test ends.
func NewStubModel(t *testing.T, text string) *StubModel {
	t.Helper()
	s := &StubModel{text: text}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()

		// One streamed turn: role, content, stop. The adapter consumes SSE, so
		// answering in that shape is what a real provider does.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`{"choices":[{"delta":{"role":"assistant"},"finish_reason":null}]}`)
		write(fmt.Sprintf(`{"choices":[{"delta":{"content":%q},"finish_reason":null}]}`, s.text))
		write(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		write(`[DONE]`)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// BaseURL is what provider.json's base_url wants (the adapter appends the path).
func (s *StubModel) BaseURL() string { return s.srv.URL + "/v1" }

// Requests is every decoded request body, for asserting what the worker sent.
func (s *StubModel) Requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.requests...)
}

// WriteProviderTo points the sandbox's provider config at baseURL. The type
// "deepseek" is the OpenAI-compatible chat-completions adapter; the key is a
// placeholder because the stub never checks it.
func WriteProviderTo(t *testing.T, s *Sandbox, baseURL string) {
	t.Helper()
	s.WriteFile("common/providers/provider.json", fmt.Sprintf(`{
  "active": "stub",
  "providers": [
    {
      "name": "stub",
      "type": "deepseek",
      "api_key": "sk-e2e-placeholder",
      "base_url": %q,
      "model": "stub-model",
      "models": [{"name": "stub-model", "context_window": 8192}]
    }
  ]
}
`, baseURL))
}
