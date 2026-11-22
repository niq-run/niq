package gateway

import (
	"errors"
	"net"
	"testing"
)

// TestBindReportsPortInUse pins the contract the CLI front-end relies on: a
// taken address is a typed, testable fact, not just a formatted string.
func TestBindReportsPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	c := New(addr)
	if _, err := c.Bind(); err == nil {
		t.Fatal("bind on a taken address succeeded")
	} else {
		var inUse *PortInUseError
		if !errors.As(err, &inUse) {
			t.Fatalf("bind error = %v, want *PortInUseError", err)
		}
		if inUse.Addr != addr {
			t.Fatalf("PortInUseError.Addr = %q, want %q", inUse.Addr, addr)
		}
		if !IsPortInUse(err) {
			t.Fatalf("IsPortInUse(%v) = false", err)
		}
	}
}

// TestIsPortInUseIgnoresOtherErrors keeps the "offer a choice" path from
// firing on a permission or bad-address failure, which the operator cannot fix
// by stopping a process.
func TestIsPortInUseIgnoresOtherErrors(t *testing.T) {
	for _, err := range []error{
		errors.New("socket: permission denied"),
		&net.AddrError{Err: "too many colons", Addr: "::1:9527"},
	} {
		if IsPortInUse(err) {
			t.Fatalf("IsPortInUse(%v) = true", err)
		}
	}
	if IsPortInUse(nil) {
		t.Fatal("IsPortInUse(nil) = true")
	}
}
