package helper

import (
	"net"
	"testing"
)

func TestPort(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want int
	}{
		{"127.0.0.1:9527", 9527},
		{":9527", 9527},
		{"[::1]:9527", 9527},
		{"9527", 9527},
		{"nonsense", 0},
		{"", 0},
		{"host:", 0},
	} {
		if got := Port(tc.addr); got != tc.want {
			t.Fatalf("Port(%q) = %d, want %d", tc.addr, got, tc.want)
		}
	}
}

func TestDialAddr(t *testing.T) {
	for in, want := range map[string]string{
		":9527":          "127.0.0.1:9527",
		"0.0.0.0:9527":   "127.0.0.1:9527",
		"[::]:9527":      "127.0.0.1:9527",
		"127.0.0.1:8080": "127.0.0.1:8080",
		"10.0.0.2:80":    "10.0.0.2:80",
		"nonsense":       "",
	} {
		if got := DialAddr(in); got != want {
			t.Fatalf("DialAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsLoopbackBind(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9527", true},
		{"localhost:9527", false}, // conservative: unresolvable name → not local-only
		{":9527", false},          // wildcard → remote-reachable
		{"0.0.0.0:9527", false},
		{"[::]:9527", false},
		{"192.168.1.10:9527", false},
		{"[::1]:9527", true},
	} {
		if got := IsLoopbackBind(tc.addr); got != tc.want {
			t.Fatalf("IsLoopbackBind(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestLoopbackBind(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"0.0.0.0:9527":      "127.0.0.1:9527",
		":9527":             "127.0.0.1:9527",
		"192.168.1.10:8080": "127.0.0.1:8080",
		"not-an-addr":       "not-an-addr",
	} {
		if got := LoopbackBind(in); got != want {
			t.Fatalf("LoopbackBind(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEphemeralAddr is the property behind a taken port: the fallback must not
// widen (or narrow) the exposure that was requested.
func TestEphemeralAddr(t *testing.T) {
	for _, tc := range []struct{ req, want string }{
		{"127.0.0.1:19831", "127.0.0.1:0"},
		{"[::1]:19831", "[::1]:0"},
		{"192.168.1.10:19831", "192.168.1.10:0"},
		{":19831", ":0"},
		{"0.0.0.0:19831", ":0"},
		{"[::]:19831", ":0"},
		{"not-an-addr", ":0"},
	} {
		if got := EphemeralAddr(tc.req); got != tc.want {
			t.Fatalf("EphemeralAddr(%q) = %q, want %q", tc.req, got, tc.want)
		}
	}
}

func TestValidPort(t *testing.T) {
	for n, want := range map[int]bool{-1: false, 0: false, 1: true, 9527: true, 65535: true, 65536: false} {
		if got := ValidPort(n); got != want {
			t.Fatalf("ValidPort(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestIsServing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if !IsServing(addr) {
		t.Fatalf("IsServing(%q) = false for a live listener", addr)
	}
	if !IsServingPort(ln.Addr().(*net.TCPAddr).Port) {
		t.Fatal("IsServingPort = false for a live listener")
	}
	ln.Close()
	if IsServing(addr) {
		t.Fatalf("IsServing(%q) = true after close", addr)
	}
	if IsServing("nonsense") {
		t.Fatal("IsServing(nonsense) = true")
	}
}

// TestProbeListenReportsBusy pins the pre-flight: a taken address must fail
// before the caller spawns anything.
func TestProbeListenReportsBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	if err := ProbeListen(ln.Addr().String()); err == nil {
		t.Fatal("ProbeListen succeeded on a taken address")
	}
	if err := ProbeListen("127.0.0.1:0"); err != nil {
		t.Fatalf("ProbeListen on a dynamic address: %v", err)
	}
}
