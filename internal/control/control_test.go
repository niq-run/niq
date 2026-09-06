package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/internal/project"
	"github.com/niq-run/niq/internal/webui"
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

func setupProjectsRoot(t *testing.T) {
	// Isolate the projects root under a temp ~/.niq by pointing HOME there,
	// so tests never touch a real ~/.niq/projects.
	t.Setenv("HOME", t.TempDir())
}

// fakeTemplate returns a small TemplateConfig usable as a project template.
func fakeTemplate() *project.TemplateConfig {
	return &project.TemplateConfig{Workers: []project.WorkerConfig{
		{Type: "hiw", ID: "default-hiw"},
		{Type: "reason", ID: "niq", Provider: "volcan-ark", Model: "deepseek-v4-flash"},
	}}
}

// TestControlContextAndList verifies the control-plane exposes the SPA mode
// context and lists projects, isolated under a temp HOME.
func TestControlContextAndList(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := project.CreateProject("alpha", fakeTemplate()); err != nil {
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
		t.Fatalf("templates status=%d", code)
	}
	if !strings.Contains(body, "default") {
		t.Fatalf("templates missing default: %s", body)
	}
}

// TestControlCreateRejectsDuplicate asserts re-creating an existing project
// returns 409 before any subprocess is launched.
func TestControlCreateRejectsDuplicate(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := project.CreateProject("taken", fakeTemplate()); err != nil {
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
	if _, err := project.LoadProject("x"); err == nil {
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
	_ = os.RemoveAll(filepath.Join(project.TemplatesDir(), "t1.json"))
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
	base := newControl(t)

	req, _ := http.NewRequest("POST", base+"/api/projects/nope/start", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

// TestControlTemplateFromProject verifies the project-export flow: preview
// (no write), creating a template from an edited draft body (POST with
// template), updating it (PUT), and the validation paths.
func TestControlTemplateFromProject(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := project.CreateProject("alpha", fakeTemplate()); err != nil {
		t.Fatal(err)
	}
	base := newControl(t)

	do := func(method, path, body string) (int, string) {
		var req *http.Request
		if body == "" {
			req, _ = http.NewRequest(method, base+path, nil)
		} else {
			req, _ = http.NewRequest(method, base+path, strings.NewReader(body))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Preview returns the exported workers without creating a template.
	code, body := do("GET", "/api/projects/alpha/template-preview", "")
	if code != 200 || !strings.Contains(body, `"niq"`) || !strings.Contains(body, "volcan-ark") {
		t.Fatalf("preview status=%d body=%s", code, body)
	}
	if _, err := os.Stat(filepath.Join(project.TemplatesDir(), "t1.json")); err == nil {
		t.Fatal("preview must not write a template file")
	}

	// Save the draft (as the edited webui drawer would) under a new id.
	draft := `{"id":"t1","template":{"workers":[{"type":"hiw","id":"default-hiw"},{"type":"reason","id":"niq","instruction":"edited goal"}]}}`
	if code, _ = do("POST", "/api/templates", draft); code != 201 {
		t.Fatalf("create from draft status=%d, want 201", code)
	}
	_, body = do("GET", "/api/templates/t1", "")
	if !strings.Contains(body, "edited goal") {
		t.Fatalf("saved draft body: %s", body)
	}

	// Edit the saved template through PUT.
	if code, _ = do("PUT", "/api/templates/t1", `{"workers":[{"type":"hiw","id":"default-hiw"}]}`); code != 204 {
		t.Fatalf("put status=%d, want 204", code)
	}
	_, body = do("GET", "/api/templates/t1", "")
	if strings.Contains(body, "edited goal") {
		t.Fatalf("put should have replaced the body: %s", body)
	}

	// Validation: PUT of a nonexistent template, a workerless body, and a
	// create with both/no source.
	if code, _ = do("PUT", "/api/templates/nope", `{"workers":[]}`); code != 404 {
		t.Fatalf("put missing status=%d, want 404", code)
	}
	if code, _ = do("PUT", "/api/templates/t1", `{"workers":[]}`); code != 400 {
		t.Fatalf("put empty workers status=%d, want 400", code)
	}
	if code, _ = do("POST", "/api/templates", `{"id":"t2"}`); code != 400 {
		t.Fatalf("no source status=%d, want 400", code)
	}
	if code, _ = do("POST", "/api/templates", `{"id":"t2","copy_from":"default","template":{"workers":[{"type":"hiw","id":"h"}]}}`); code != 400 {
		t.Fatalf("two sources status=%d, want 400", code)
	}
	if code, _ = do("POST", "/api/templates", draft); code != 409 {
		t.Fatalf("duplicate status=%d, want 409", code)
	}
}

// TestControlProviders verifies the provider config endpoints: GET returns the
// file's content (the example skeleton seeded on first run), PUT replaces it,
// and structural validation rejects duplicate names.
func TestControlProviders(t *testing.T) {
	setupProjectsRoot(t)
	base := newControl(t)

	do := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// GET on a missing file returns an empty config, not an error.
	code, body := do("GET", "/api/providers", "")
	if code != 200 {
		t.Fatalf("get status=%d: %s", code, body)
	}

	// PUT a config, then read it back.
	cfg := `{"active":"p2","providers":[{"name":"p1","type":"deepseek"},{"name":"p2","type":"claude","api_key":"k"}]}`
	if code, _ = do("PUT", "/api/providers", cfg); code != 204 {
		t.Fatalf("put status=%d, want 204", code)
	}
	_, body = do("GET", "/api/providers", "")
	if !strings.Contains(body, `"p2"`) || !strings.Contains(body, `"active":"p2"`) {
		t.Fatalf("round-trip body: %s", body)
	}

	// Structural validation: duplicate names are rejected.
	if code, _ = do("PUT", "/api/providers", `{"providers":[{"name":"x"},{"name":"x"}]}`); code != 400 {
		t.Fatalf("duplicate name status=%d, want 400", code)
	}
	if code, _ = do("PUT", "/api/providers", `{"providers":[{"type":"deepseek"}]}`); code != 400 {
		t.Fatalf("missing name status=%d, want 400", code)
	}
}
