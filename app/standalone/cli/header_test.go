package cli

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/store"
)

// TestControlHeaderShowsBasicInfo pins that the running control-plane header —
// which both `niq` and `niq i` lead with — carries the gateway state and the
// commands that act on it.
func TestControlHeaderShowsBasicInfo(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	st := &shellState{PID: 4242, Bound: "127.0.0.1:9527", Started: "2024-01-02T03:04:05Z"}

	h := controlHeader(st)
	for _, want := range []string{"gateway", "running", "pid 4242", "webui", "stop", "restart"} {
		if !strings.Contains(h, want) {
			t.Fatalf("control header missing %q:\n%s", want, h)
		}
	}
}
