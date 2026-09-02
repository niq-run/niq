package httptrans

import (
	"context"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/eventbus"
)

// TestEventsHeadersFlushWithoutTraffic verifies that a connect to /events
// returns its response headers immediately, even with no event ever delivered.
// A regression guard for the SSE handshake: without flushing headers up front,
// an idle worker's bus connection would not complete until some other bus
// traffic happened to deliver an event to it (see handleEvents).
func TestEventsHeadersFlushWithoutTraffic(t *testing.T) {
	log.SetOutput(io.Discard)

	reg, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "id.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	defer reg.Close()
	if err := reg.Register(corebus.Identity{
		WorkerID:       "w1",
		Type:           "lark",
		Credential:     "cred",
		PublishAllow:   []string{"*"},
		SubscribeAllow: []event.EventPattern{{Type: "*"}},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	eng := eventbus.NewEngine(reg, nil)
	srv := NewServer(eng, reg, ":0")
	addr, err := srv.Bind()
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	url := "http://" + addr + "/events?worker_id=w1&credential=cred"

	// The request must complete (headers received) without any event traffic.
	// Before the flush fix this hung until the client-side timeout.
	client := &http.Client{Timeout: 3 * time.Second}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /events did not respond promptly (<3s), got: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Fatalf("headers took %s to arrive under no traffic (should be immediate)", elapsed)
	}
}
