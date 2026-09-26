package bus

import "errors"

// Delivery errors returned by a BusSideChannel.Send when it cannot, or
// chooses not to, deliver. The engine logs them and moves on; neither is
// fatal.
var (
	// ErrClosed is returned when the worker has already disconnected and its
	// channel is closed. Sending to a closed Go channel panics ("send on
	// closed channel"), so a Send guards against Close under the same mutex
	// and returns this checked error instead — never a crash.
	ErrClosed = errors.New("eventbus: channel closed")

	// ErrDropped is returned when a worker's outbound buffer is full and the
	// delivery is dropped rather than blocking the whole bus behind one slow
	// consumer. The drop is counted for observability.
	ErrDropped = errors.New("eventbus: dropped (channel buffer full)")
)