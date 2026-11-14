package project

import "testing"

// TestEphemeralAddr verifies the bind fallback keeps the requested exposure: a
// taken loopback port must not silently widen the listener to all interfaces,
// while wildcard and explicit LAN binds keep their (already remote) host.
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
		if got := ephemeralAddr(tc.req); got != tc.want {
			t.Fatalf("ephemeralAddr(%q) = %q, want %q", tc.req, got, tc.want)
		}
	}
}
