package webui

import (
	"context"
	"net"
	"testing"
)

// New only dereferences its dependencies inside route handlers, so a bare
// server with nil deps is enough to exercise bind/serve. Established here so
// we test dynamic-port allocation without standing up a full HIW/engine.
func TestBindDynamicWebUIPort(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if addr == ":0" || addr == "" {
		t.Fatalf("resolved addr = %q, want a real host:port", addr)
	}
	if got := s.ResolvedAddr(); got != addr {
		t.Fatalf("ResolvedAddr = %q, want %q", got, addr)
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	conn.Close()

	again, err := s.Bind()
	if err != nil || again != addr {
		t.Fatalf("second Bind = %q (%v), want %q", again, err, addr)
	}
}

func TestWebUIServeOnBoundListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(nil, nil, nil, nil, nil, ":0", false)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

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