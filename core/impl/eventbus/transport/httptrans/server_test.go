package httptrans

import (
	"context"
	"net"
	"testing"
)

// TestBindDynamicPort verifies ":0" yields an ephemeral, already-listening port
// that is non-zero, readable via ResolvedAddr, and dialable.
func TestBindDynamicPort(t *testing.T) {
	srv := NewServer(nil, nil, ":0")
	addr, err := srv.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if addr == ":0" || addr == "" {
		t.Fatalf("resolved addr = %q, want a real host:port", addr)
	}
	if got := srv.ResolvedAddr(); got != addr {
		t.Fatalf("ResolvedAddr = %q, want %q", got, addr)
	}
	// Listener is live before Serve.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial bound listener %s: %v", addr, err)
	}
	conn.Close()

	// Bind twice returns the same address (no double-bind / port leak).
	again, err := srv.Bind()
	if err != nil || again != addr {
		t.Fatalf("second Bind = %q (%v), want %q", again, err, addr)
	}
}

// TestServeOnBoundListener verifies Start serves on the pre-bound listener and
// returns cleanly on ctx cancellation.
func TestServeOnBoundListener(t *testing.T) {
	srv := NewServer(nil, nil, ":0")
	addr, err := srv.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	conn.Close()

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned %v", err)
	}
}