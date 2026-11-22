package helper

import (
	"net"
	"strconv"
	"strings"
	"time"
)

// dialTimeout bounds a liveness probe: a peer that accepts is instant, so
// anything slower is "not serving" as far as niq's checks are concerned.
const dialTimeout = 300 * time.Millisecond

// Port reads the numeric port of an address in any of the shapes niq passes
// around — "host:port", ":port", "[::1]:port", or a bare "port" — and returns
// 0 when there is none.
func Port(addr string) int {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		if n, err := strconv.Atoi(port); err == nil {
			return n
		}
	}
	// A user may type just the number, and project.json stores ports as ints
	// that are re-rendered into addresses elsewhere.
	if n, err := strconv.Atoi(strings.TrimPrefix(addr, ":")); err == nil {
		return n
	}
	return 0
}

// DialAddr maps a listen address to something dialable from this machine: an
// unspecified host (":9527", "0.0.0.0:9527", "[::]:9527") means loopback.
// Empty when addr has no port part.
func DialAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// IsServing reports whether something accepts a connection on addr.
func IsServing(addr string) bool {
	target := DialAddr(addr)
	if target == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", target, dialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// IsServingPort is IsServing for a loopback port.
func IsServingPort(port int) bool {
	if port <= 0 {
		return false
	}
	return IsServing(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// ProbeListen reports whether addr can be bound right now, closing the probe
// listener immediately: this is a pre-flight, not the real bind.
func ProbeListen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

// IsLoopbackBind reports whether addr binds exclusively to the loopback
// interface (127.0.0.1 / ::1), i.e. remote hosts cannot reach it. Wildcard and
// explicit non-loopback addrs are not loopback-only.
func IsLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// LoopbackBind keeps addr on loopback, preserving its port. An empty address
// stays empty (the server is disabled).
func LoopbackBind(addr string) string {
	if addr == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// EphemeralAddr maps addr to port 0 on the same host, so a fallback keeps the
// exposure that was requested: loopback stays loopback (a data port must not
// widen to all interfaces just because its port was taken), while wildcard and
// explicit LAN binds keep that interface.
func EphemeralAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return ":0" // already the widest exposure
	}
	return net.JoinHostPort(host, "0")
}

// ValidPort reports whether n is a usable TCP port number.
func ValidPort(n int) bool { return n > 0 && n <= 65535 }
