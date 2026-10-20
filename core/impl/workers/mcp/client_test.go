package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// channel is a minimal WorkerSideChannel used only so worker construction
// (which stores the channel without using it) does not need a real bus.
type channel struct{}

func (c channel) ID() string                                               { return "none" }
func (c channel) Send(_ context.Context, _ event.Event, _ ...string) error { return nil }
func (c channel) Broadcast(_ context.Context, _ event.Event) error         { return nil }
func (c channel) Receive(context.Context) (<-chan event.Event, error) {
	ch := make(chan event.Event)
	close(ch)
	return ch, nil
}
func (c channel) Close() error { return nil }

var _ corebus.WorkerSideChannel = channel{}

// newTestMCPServer serves a go-sdk MCP server with an "echo" tool, optionally
// requiring an Authorization header.
func newTestMCPServer(t *testing.T, requireAuth string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "echo a message",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"text": map[string]any{"type": "string"},
		}},
		// structured output: the result shape a caller can plan against, and
		// what the worker republishes as the capability's Returns.
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"echoed": map[string]any{"type": "string"},
		}},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if req.Params != nil {
			_ = json.Unmarshal(req.Params.Arguments, &args)
		}
		text, _ := args["text"].(string)
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: "echo: " + text}},
			StructuredContent: map[string]any{"echoed": text},
		}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, nil)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireAuth != "" && r.Header.Get("Authorization") != requireAuth {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
}

func TestServerClientListAndCall(t *testing.T) {
	ts := newTestMCPServer(t, "")
	defer ts.Close()

	cl := newServerClient(ServerConfig{Name: "test", URL: ts.URL})
	defer cl.Close()
	if err := cl.EnsureConnected(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	tools, err := cl.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
	// The declared output schema must survive the projection: it becomes the
	// capability's Returns, which is how an agent (or a PTC script) knows what
	// shape to expect back.
	if out, ok := tools[0].OutputSchema.(map[string]any); !ok || out["type"] != "object" {
		t.Fatalf("OutputSchema = %#v, want the declared schema", tools[0].OutputSchema)
	}
	out, err := cl.CallTool(context.Background(), "echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	// Text plus the structured half: the caller parses the JSON tail.
	if !strings.HasPrefix(out, "echo: hi") || !strings.Contains(out, `"echoed":"hi"`) {
		t.Fatalf("call result = %q, want the text and its structured content", out)
	}
}

func TestServerClientHeaders(t *testing.T) {
	ts := newTestMCPServer(t, "Bearer sekret")
	defer ts.Close()

	cl := newServerClient(ServerConfig{Name: "test", URL: ts.URL, Headers: map[string]string{"Authorization": "Bearer sekret"}})
	defer cl.Close()
	if _, err := cl.ListTools(context.Background()); err != nil {
		t.Fatalf("list with auth header: %v", err)
	}
}

func TestServerClientRejectsMissingAuth(t *testing.T) {
	ts := newTestMCPServer(t, "Bearer sekret")
	defer ts.Close()

	cl := newServerClient(ServerConfig{Name: "test", URL: ts.URL})
	defer cl.Close()
	if _, err := cl.ListTools(context.Background()); err == nil {
		t.Fatal("expected error without auth header when server requires it")
	}
}
