// Package http provides a tool-gateway worker. It exposes webfetch for
// fetching remote content and can be extended via MCP servers — when
// configured, the tools discovered from MCP servers are merged into
// this worker's published tool list.
package http

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/baseworker"
)

// Config holds configuration for an HTTP worker.
type Config struct {
	ID   string
	Bus  corebus.WorkerSideChannel
	HTTP *http.Client // defaults to a 30s timeout client

	// MCP config path — when set, the worker connects to the referenced
	// MCP servers at startup and merges their tools into its own list.
	MCPConfig string
}

// Worker is a bus-connected HTTP tool gateway.
type Worker struct {
	baseworker.BaseWorker
	client    *http.Client
	started   bool
	cancelRun context.CancelFunc
	mu        sync.Mutex

	mcpConfig  string
	mcpServers []mcpConn
}

// New creates an HTTP tool gateway worker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "http"
	}
	c := cfg.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	return &Worker{
		BaseWorker: baseworker.NewBaseWorker(id, []event.EventPattern{
			event.NewPattern(event.TypeToolRequest),
			event.NewPattern(event.TypeToolCancel),
			event.NewPattern(event.TypeWorkerDiscover),
		}, cfg.Bus),
		client:    c,
		mcpConfig: cfg.MCPConfig,
	}
}

func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("http worker %s: already started", w.ID())
	}
	w.connectMCP(ctx)

	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancelRun = cancelFn
	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.publishReady()
	w.started = true
	log.Printf("[http worker %s] started (mcp servers: %d)", w.ID(), len(w.mcpServers))
	return nil
}

func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	w.cancelRun()
	w.cancelRun = nil
	w.started = false
	return nil
}

func (w *Worker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *Worker) Restore(state []byte) error { return nil }

// ── Event loop ──

func (w *Worker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) process(evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerDiscover:
		w.publishReady()
	case event.TypeToolCancel:
		callID, _ := evt.Payload["call_id"].(string)
		log.Printf("[http worker %s] cancel requested for %s (best-effort)", w.ID(), callID)
	case event.TypeToolRequest:
		if evt.TargetWorkerID != "" && evt.TargetWorkerID != w.ID() {
			return
		}
		w.handleToolCall(evt)
	}
}

func (w *Worker) handleToolCall(evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	name, _ := evt.Payload["name"].(string)
	callerID := evt.WorkerId
	args, _ := evt.Payload["arguments"].(map[string]any)

	var result string
	var err error
	switch name {
	case "webfetch":
		result, err = w.handleWebfetch(args)
	default:
		handled, res, mcperr := w.tryMCPTool(name, args)
		if handled {
			result, err = res, mcperr
		} else {
			err = fmt.Errorf("http worker: unknown tool %s", name)
		}
	}
	if err != nil {
		w.publishFailed(callerID, callID, name, err)
		return
	}
	w.publishCompleted(callerID, callID, name, result)
}

// ── Bus publishing ──

func (w *Worker) publishReady() {
	tools := []map[string]any{
		{
			"name":        "webfetch",
			"description": "Fetch the contents of a URL and return the raw response body (up to 1 MB). Follows redirects automatically.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "The URL to fetch."},
				},
				"required": []any{"url"},
			},
		},
	}
	for _, srv := range w.mcpServers {
		for _, t := range srv.tools {
			tools = append(tools, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			})
		}
	}
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "http",
		"tools":     tools,
	}))
}

func (w *Worker) publishCompleted(callerID, callID, toolName, result string) {
	evt := event.New(event.TypeToolCompleted, w.ID(), map[string]any{
		"call_id": callID,
		"name":    toolName,
		"result":  result,
	})
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

func (w *Worker) publishFailed(callerID, callID, toolName string, err error) {
	evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
		"call_id": callID,
		"name":    toolName,
		"error":   err.Error(),
	})
	_ = w.Channel.Send(context.Background(), evt, callerID)
}
