package httptrans

import (
	"context"
	"testing"
	"time"
)

// TestClientStreamStaysOpen verifies a single connected client's SSE stream stays
// open (no server-side churn) — the isolation harness for the reconnect flap.
func TestClientStreamStaysOpen(t *testing.T) {
	signer, srv := newAuthHarness(t)
	tok, _ := signer.Mint("remote-w", time.Hour)

	addr, err := srv.Bind()
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	serveCtx, stop := context.WithCancel(context.Background())
	go func() { _ = srv.Start(serveCtx) }()
	t.Cleanup(stop)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	side := NewWorkerSide("http://"+addr, "remote-w", tok)
	if err := side.Connect(ctx, "http://"+addr); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer side.Close()
	ch, err := side.Receive(ctx)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	deadline := time.After(4 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatal("client stream closed on its own")
			}
		case <-time.After(500 * time.Millisecond):
		case <-deadline:
			// Still open after 4s: the transport keeps a single connection alive.
			return
		}
	}
}
