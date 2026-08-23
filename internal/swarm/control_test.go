package swarm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/54c1/niq/internal/swarm/webui"
)

func doGet(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestControlContextAndList verifies the control-plane exposes the SPA mode
// context and lists projects, isolated under a temp HOME.
func TestControlContextAndList(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("alpha", fakeTemplate()); err != nil {
		t.Fatal(err)
	}

	c := NewControl("127.0.0.1:0")
	addr, err := c.Bind()
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Start(ctx) }()
	base := "http://" + addr

	// Context reports control mode.
	code, body := doGet(t, base+"/api/context")
	if code != 200 {
		t.Fatalf("context status=%d", code)
	}
	var ci webui.ContextInfo
	if err := json.Unmarshal([]byte(body), &ci); err != nil {
		t.Fatalf("context parse: %v (%s)", err, body)
	}
	if ci.Mode != "control" {
		t.Fatalf("mode=%q, want control", ci.Mode)
	}

	// Projects list includes alpha.
	code, body = doGet(t, base+"/api/projects")
	if code != 200 {
		t.Fatalf("projects status=%d: %s", code, body)
	}
	if !strings.Contains(body, `"id":"alpha"`) {
		t.Fatalf("projects list missing alpha: %s", body)
	}
}

// newControl starts a Control on an ephemeral port and returns the base URL.
func newControl(t *testing.T) string {
	t.Helper()
	c := NewControl("127.0.0.1:0")
	addr, err := c.Bind()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Start(ctx) }()
	return "http://" + addr
}

// TestControlTemplates verifies the template list is served (default is always
// available, embedded if not yet seeded).
func TestControlTemplates(t *testing.T) {
	base := newControl(t)
	code, body := doGet(t, base+"/api/templates")
	if code != 200 {
		t.Fatalf("templates status=%d: %s", code, body)
	}
	if !strings.Contains(body, "default") {
		t.Fatalf("templates missing default: %s", body)
	}
}

// TestControlCreateRejectsDuplicate asserts re-creating an existing project
// returns 409 before any subprocess is launched.
func TestControlCreateRejectsDuplicate(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("taken", fakeTemplate()); err != nil {
		t.Fatal(err)
	}
	base := newControl(t)

	resp, err := http.Post(base+"/api/projects", "application/json", strings.NewReader(`{"id":"taken","template":"default"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate create status=%d, want 409", resp.StatusCode)
	}
}

// TestControlCreateRejectsUnknownTemplate asserts a bad template name returns 400
// without creating anything.
func TestControlCreateRejectsUnknownTemplate(t *testing.T) {
	setupProjectsRoot(t)
	base := newControl(t)

	resp, err := http.Post(base+"/api/projects", "application/json", strings.NewReader(`{"id":"x","template":"no-such-template"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("unknown template status=%d, want 400", resp.StatusCode)
	}
	if _, err := LoadProject("x"); err == nil {
		t.Fatal("project should not have been created")
	}
}

// TestControlTemplateCloneDelete verifies the template clone + delete endpoints
// (isolated under a temp HOME; default is seeded from the embedded built-in).
func TestControlTemplateCloneDelete(t *testing.T) {
	setupProjectsRoot(t)
	base := newControl(t)

	// Clone default -> t1.
	resp, err := http.Post(base+"/api/templates", "application/json", strings.NewReader(`{"id":"t1","copy_from":"default"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("clone status=%d, want 201", resp.StatusCode)
	}
	_, body := doGet(t, base+"/api/templates")
	if !strings.Contains(body, "t1") {
		t.Fatalf("templates missing t1: %s", body)
	}

	// Delete t1.
	req, _ := http.NewRequest("DELETE", base+"/api/templates/t1", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("delete status=%d, want 204", resp.StatusCode)
	}
	_, body = doGet(t, base+"/api/templates")
	if strings.Contains(body, "t1") {
		t.Fatalf("templates still has t1: %s", body)
	}
}

// TestControlStopNotRunning asserts stopping a project with no live process
// returns 404.
func TestControlStopNotRunning(t *testing.T) {
	setupProjectsRoot(t)
	base := newControl(t)

	resp, err := http.Post(base+"/api/projects/x/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("stop not-running status=%d, want 404", resp.StatusCode)
	}
}

// TestControlStartProjectNotFound asserts an unknown project id is rejected
// before any subprocess is attempted.
func TestControlStartProjectNotFound(t *testing.T) {
	setupProjectsRoot(t)
	c := NewControl("127.0.0.1:0")
	addr, err := c.Bind()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Start(ctx) }()

	code, _ := doGet(t, "http://"+addr+"/api/projects/nope/start")
	_ = code
	// Start uses POST; a GET on the route returns 405 from the mux. We instead
	// assert via the project-not-found path with the real method.
	req, _ := http.NewRequest("POST", "http://"+addr+"/api/projects/nope/start", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}