//go:build e2e

package bus_e2e

import (
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/e2e/harness"
)

// newLiveProject is the setup every data-plane test needs: an isolated
// installation, a gateway on an ephemeral port, and one project whose bus is
// therefore real.
func newLiveProject(t *testing.T) (*harness.Sandbox, *harness.Gateway, string) {
	t.Helper()
	s := harness.NewSandbox(t)
	harness.WriteProvider(t, s)
	g := s.Start(t)
	const id = "bus"
	g.CreateProject(t, id)
	g.WaitProject(t, id, true)
	return s, g, id
}

// settle is for the assertions that something must NOT arrive: routing is
// asynchronous, so a negative check needs a bounded window in which it could
// have arrived.
const settle = 150 * time.Millisecond

// ofType reports whether evt is the given type.
func ofType(want string) func(event.Event) bool {
	return func(evt event.Event) bool { return string(evt.Type) == want }
}

// withRequest reports whether evt carries the given request id.
func withRequest(id string) func(event.Event) bool {
	return func(evt event.Event) bool { return evt.RequestId == id }
}

// countOf counts events of a type in a slice.
func countOf(events []event.Event, typ string) int {
	n := 0
	for _, evt := range events {
		if string(evt.Type) == typ {
			n++
		}
	}
	return n
}
