// Package helper holds the small, dependency-free primitives every
// single-machine package needs and none of them owns: who is listening on a
// port, whether an address answers, and how to keep a bind on the host that
// was asked for.
//
// It imports only the standard library, so it can sit underneath `store`
// (which uses it for address parsing) without inverting the layering. Nothing
// domain-specific belongs here: a helper that knows about projects, workers or
// auth is in the wrong package.
package helper

import "errors"

// ErrUnsupported means this platform has no port-holder discovery (no lsof).
var ErrUnsupported = errors.New("stopping the process on a port is not supported on this platform")

// Holder is one process listening on the port.
type Holder struct {
	PID     int
	Command string // process name reported by lsof; empty when unknown
}
