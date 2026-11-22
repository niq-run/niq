//go:build e2e

package cli_e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/niq-run/niq/e2e/harness"
)

// TestE2EProjectRowLinksThroughTheGateway is the regression test for the link
// the picker used to print: the project's own port serves JSON only, so the
// only address a human can open is the gateway's /p/<id>/.
func TestE2EProjectRowLinksThroughTheGateway(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	harness.WriteProvider(t, s)
	g := s.Start(t)
	g.CreateProject(t, "demo")

	row, ok := g.Project(t, "demo")
	if !ok || !row.Running {
		t.Fatalf("project row = %+v (ok=%v), want a running instance", row, ok)
	}
	if !strings.HasSuffix(row.URL, "/p/demo/") {
		t.Fatalf("url = %q, want it to end with /p/demo/", row.URL)
	}
	if strings.Contains(row.URL, fmt.Sprintf(":%d/", row.WebUI)) {
		t.Fatalf("url points at the data port: %q", row.URL)
	}

	// The gateway serves the page while the project runs…
	code, body := g.GetHTML(t, "/p/demo/")
	if code != 200 || !strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("GET /p/demo/ = %d, body starts %q", code, firstLine(body))
	}
	// …and the data port serves no page at all, which is why the link matters.
	if code := getStatus(t, fmt.Sprintf("127.0.0.1:%d", row.WebUI), "/"); code != 404 {
		t.Fatalf("the data port answered %d on /, want 404", code)
	}

	// The human report prints the same link.
	s.Run("status").RequireExit(t, 0).RequireContains(t, "/p/demo/")
}

// TestE2EProjectLinkSurvivesAStoppedInstance: /p/<id>/ is served from the
// gateway's bundle, so a stopped project still has a page (with a start button).
func TestE2EProjectLinkSurvivesAStoppedInstance(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	harness.WriteProvider(t, s)
	g := s.Start(t)
	g.CreateProject(t, "demo")
	g.StopProject(t, "demo")
	g.WaitProject(t, "demo", false)

	row, _ := g.Project(t, "demo")
	if !strings.HasSuffix(row.URL, "/p/demo/") {
		t.Fatalf("url = %q, want the gateway link even when stopped", row.URL)
	}
	if code, body := g.GetHTML(t, "/p/demo/"); code != 200 || !strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("GET /p/demo/ = %d for a stopped project, body starts %q", code, firstLine(body))
	}
}

// TestE2EStatusIDsIsScriptable: completion and prompts read ids from here.
func TestE2EStatusIDsIsScriptable(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	harness.WriteProvider(t, s)
	g := s.Start(t)
	g.CreateProject(t, "demo")

	res := s.Run("status", "--ids").RequireExit(t, 0)
	if got := strings.TrimSpace(res.Stdout); got != "demo" {
		t.Fatalf("status --ids = %q, want just the id", got)
	}
}

// TestE2ECJKProjectIDKeepsTheColumnsAligned: ids are user text, so a wide name
// must not push the state and port columns out of line.
func TestE2ECJKProjectIDKeepsTheColumnsAligned(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	harness.WriteProvider(t, s)
	g := s.Start(t)
	g.CreateProject(t, "e2e")
	g.CreateProject(t, "值班")

	out := s.Run("status").RequireExit(t, 0).Stdout
	ascii := rowFor(t, out, "e2e")
	wide := rowFor(t, out, "值班")

	if a, b := columnOf(t, ascii, "running"), columnOf(t, wide, "running"); a != b {
		t.Fatalf("state column differs: %d vs %d\n%s\n%s", a, b, ascii, wide)
	}

	// The link is escaped for the URL, and the project is still reachable.
	row, _ := g.Project(t, "值班")
	if !strings.Contains(row.URL, "%E5%80%BC%E7%8F%AD") {
		t.Fatalf("url = %q, want a percent-encoded id", row.URL)
	}
	if code, body := g.GetHTML(t, "/p/值班/"); code != 200 || !strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("GET /p/值班/ = %d, body starts %q", code, firstLine(body))
	}
}

// TestE2EStopProjectsStopsTheInstancesAndTheGateway: -p is the "leave nothing
// running" switch, and the snapshot must show both halves after it.
func TestE2EStopProjectsStopsTheInstancesAndTheGateway(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	harness.WriteProvider(t, s)
	g := s.Start(t)
	g.CreateProject(t, "demo")
	g.CreateProject(t, "other")

	s.Run("stop", "-p").RequireExit(t, 0).RequireContains(t, "stopped")

	snap := decodeStatus(t, s)
	if snap.Gateway.Running {
		t.Fatalf("gateway still running: %+v", snap.Gateway)
	}
	for _, p := range snap.Projects {
		if p.Running {
			t.Fatalf("project %s still running after stop -p", p.ID)
		}
	}
	if len(snap.Projects) != 2 {
		t.Fatalf("projects = %+v, want both kept (ports are what they are)", snap.Projects)
	}
}

// TestE2EPickerDegradesWithoutATerminal: `niq i` piped into something prints the
// list instead of waiting for arrow keys.
func TestE2EPickerDegradesWithoutATerminal(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	harness.WriteProvider(t, s)
	g := s.Start(t)
	g.CreateProject(t, "demo")

	res := s.Run("i").RequireExit(t, 0)
	res.RequireContains(t, "[demo]")
	if strings.Contains(res.Stdout, "↑/↓") {
		t.Fatalf("the picker drew its interactive block without a terminal:\n%s", res)
	}
}

// TestE2EPickerWithoutProjectsSaysSo: an empty installation gets a next step,
// not an empty screen. The gateway is started first on an ephemeral port
// because `niq i` has no --addr: it adopts a running gateway, and only falls
// back to the default address when there is none (which would collide with
// whatever else is on this machine).
func TestE2EPickerWithoutProjectsSaysSo(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	s.Start(t)

	res := s.Run("i").RequireExit(t, 0).RequireContains(t, "(no projects)")
	res.RequireContains(t, "niq w")
}

// firstLine is for failure messages: enough of a body to see what came back.
func firstLine(body string) string {
	line := strings.SplitN(strings.TrimSpace(body), "\n", 2)[0]
	if len(line) > 120 {
		line = line[:120]
	}
	return line
}
