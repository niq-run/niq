package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolInfo is the minimal projection of a remote MCP tool we surface to the bus.
type toolInfo struct {
	Name        string
	Description string
	Schema      any
	// OutputSchema is the remote tool's declared result shape (MCP structured
	// output). Kept verbatim so it can be republished as the capability's
	// Response — the reply half of request/response. An agent needs it to know
	// how to parse a call's result, and in PTC mode a script cannot be written
	// without it.
	OutputSchema any
}

// headersTransport adds request headers on top of a base RoundTripper.
// MCP Streamable HTTP auth goes through these (Authorization stored via
// ${ENV} refs / plaintext for now).
type headersTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t *headersTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, vs := range t.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return t.base.RoundTrip(req)
}

// serverClient wraps an official go-sdk client session to one remote MCP
// endpoint. It holds the transport + session; the worker manages lifecycle.
//
// A worker serves many calls at once, so a client must be safe for concurrent
// use: mu guards the session POINTERS only — establishing or dropping a session
// is serialized, a tools/list or tools/call is not. That is what makes one
// slow remote call cost nothing but its own goroutine (the SDK multiplexes
// concurrent requests over one session by JSON-RPC id). cfg is immutable per
// client: mcp.server.update installs a new client instead of mutating this one.
type serverClient struct {
	cfg        ServerConfig
	httpClient *http.Client

	mu      sync.Mutex
	client  *mcp.Client
	session *mcp.ClientSession
}

func newServerClient(cfg ServerConfig) *serverClient {
	rt := http.RoundTripper(http.DefaultTransport)
	if len(cfg.Headers) > 0 {
		h := http.Header{}
		for k, v := range cfg.Headers {
			h.Set(k, v)
		}
		rt = &headersTransport{base: http.DefaultTransport, headers: h}
	}
	return &serverClient{
		cfg:        cfg,
		httpClient: &http.Client{Transport: rt},
	}
}

// Connect establishes (or re-establishes) the MCP client session.
func (c *serverClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked(ctx)
}

// EnsureConnected connects if not already connected.
func (c *serverClient) EnsureConnected(ctx context.Context) error {
	_, err := c.sessionFor(ctx)
	return err
}

// connected reports whether a session exists, without connecting.
func (c *serverClient) connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session != nil
}

// sessionFor returns a live session, connecting when there is none. The
// returned session is used OUTSIDE the lock: concurrent calls share it.
func (c *serverClient) sessionFor(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		return c.session, nil
	}
	if err := c.connectLocked(ctx); err != nil {
		return nil, err
	}
	return c.session, nil
}

// connectLocked replaces the session, closing the previous one first (it may
// belong to an endpoint that was just updated). Caller holds c.mu.
func (c *serverClient) connectLocked(ctx context.Context) error {
	if c.session != nil {
		_ = c.session.Close()
		c.session = nil
	}
	c.client = nil
	client := mcp.NewClient(&mcp.Implementation{Name: "niq-mcp-worker", Version: "0.1.0"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   c.cfg.URL,
		HTTPClient: c.httpClient,
	}, nil)
	if err != nil {
		if sess != nil {
			_ = sess.Close()
		}
		return fmt.Errorf("mcp: connect %s: %w", c.cfg.Name, err)
	}
	c.client = client
	c.session = sess
	return nil
}

func (c *serverClient) ListTools(ctx context.Context) ([]toolInfo, error) {
	sess, err := c.sessionFor(ctx)
	if err != nil {
		return nil, err
	}
	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		c.dropSession(sess)
		return nil, fmt.Errorf("mcp: %s tools/list: %w", c.cfg.Name, err)
	}
	out := make([]toolInfo, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, toolInfo{
			Name:         t.Name,
			Description:  t.Description,
			Schema:       t.InputSchema,
			OutputSchema: t.OutputSchema,
		})
	}
	return out, nil
}

func (c *serverClient) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	sess, err := c.sessionFor(ctx)
	if err != nil {
		return "", err
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		c.dropSession(sess)
		return "", fmt.Errorf("mcp: %s tools/call %s: %w", c.cfg.Name, name, err)
	}
	return renderResult(res), nil
}

// renderResult flattens a CallToolResult into plain text for the request.completed reply.
func renderResult(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, content := range res.Content {
		switch ct := content.(type) {
		case *mcp.TextContent:
			sb.WriteString(ct.Text)
		default:
			if b, err := json.Marshal(ct); err == nil {
				sb.Write(b)
			}
		}
		sb.WriteString("\n")
	}
	// Structured output is appended even when text content is present: the MCP
	// tools that declare outputSchema routinely return both a human-readable
	// text block and the same data as structuredContent, and a caller (or a PTC
	// script planning its parse) needs the structured half.
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.Write(b)
		}
	}
	return strings.TrimSpace(sb.String())
}

// dropSession forgets sess after a failed request, so the next call reconnects.
// The close happens OUTSIDE the lock — it is a network round trip and must not
// hold up the other calls on this server. A session that already changed under
// us is left alone: that is a reconnect another goroutine performed.
func (c *serverClient) dropSession(sess *mcp.ClientSession) {
	c.mu.Lock()
	if c.session != sess {
		c.mu.Unlock()
		return
	}
	c.session, c.client = nil, nil
	c.mu.Unlock()
	_ = sess.Close()
}

func (c *serverClient) Close() {
	c.mu.Lock()
	sess := c.session
	c.session, c.client = nil, nil
	c.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}
