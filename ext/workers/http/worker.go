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
// The http worker's tools, each its own event type.
const TypeWebfetch event.EventType = "webfetch"

func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "http"
	}
	c := cfg.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(id, cfg.Bus),
		client:     c,
		mcpConfig:  cfg.MCPConfig,
	}
	w.registerExtensions()
	return w
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
	w.AnnounceReady("http", nil)
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
		w.AnnounceReady("http", nil)
	case event.TypeRequestCancel:
		callID := evt.RequestId
		log.Printf("[http worker %s] cancel requested for %s (best-effort)", w.ID(), callID)
	default:
		if !w.DispatchExtension(evt) {
			log.Printf("[http worker %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}

// registerExtensions declares the http worker's tools: each is an extension
// served by its own event type, announced to peers via AnnounceReady.
func (w *Worker) registerExtensions() {
	w.Register(baseworker.Extension{
		Event:       TypeWebfetch,
		Description: "Fetch the contents of a URL and return the raw response body (up to 1 MB). Follows redirects automatically.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "The URL to fetch."},
			},
			"required": []any{"url"},
		},
	}, func(evt event.Event) {
		callID := evt.RequestId
		name := string(evt.Type)
		callerID := evt.WorkerId
		args := evt.Payload
		result, err := w.handleWebfetch(args)
		if err != nil {
			w.publishFailed(callerID, callID, name, err)
			return
		}
		w.publishCompleted(callerID, callID, name, result)
	})
}

func (w *Worker) publishCompleted(callerID, callID, toolName, result string) {
	evt := event.New(event.TypeRequestCompleted, w.ID(), map[string]any{
		"name":   toolName,
		"result": result,
	})
	evt.RequestId = callID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

func (w *Worker) publishFailed(callerID, callID, toolName string, err error) {
	evt := event.New(event.TypeRequestFailed, w.ID(), map[string]any{
		"name":  toolName,
		"error": err.Error(),
	})
	evt.RequestId = callID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}
