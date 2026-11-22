package harness

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// providerJSON is a syntactically complete config with a placeholder key.
//
// The startup gate only asks whether *some* provider is configured, so this is
// enough to let a project with a reason worker come up. No test sends a message
// that would reach a model, and the base URL points at a host nothing listens
// to, so an accidental call fails fast rather than leaving the machine.
const providerJSON = `{
  "active": "stub",
  "providers": [
    {
      "name": "stub",
      "type": "deepseek",
      "api_key": "sk-e2e-placeholder",
      "base_url": "http://127.0.0.1:9/v1",
      "model": "stub-model",
      "models": [{"name": "stub-model", "context_window": 8192}]
    }
  ]
}
`

// WriteProvider seeds the provider config the startup gate looks for.
func WriteProvider(t *testing.T, s *Sandbox) {
	t.Helper()
	s.WriteFile("common/providers/provider.json", providerJSON)
}

// CreateProject creates and starts a project through the gateway, the way a
// client does. The template is the built-in default. The call returns once the
// instance answers (the gateway waits for it), so a follow-up assertion can use
// the ports immediately.
func (g *Gateway) CreateProject(t *testing.T, id string) {
	t.Helper()
	code, body := g.PostJSON(t, "/api/projects", map[string]string{"id": id, "template": "default"})
	if code != http.StatusOK {
		t.Fatalf("create project %q: %d %s", id, code, body)
	}
}

// StartProject starts an existing project.
func (g *Gateway) StartProject(t *testing.T, id string) {
	t.Helper()
	code, body := g.PostJSON(t, "/api/projects/"+id+"/start", nil)
	if code != http.StatusOK && code != http.StatusAccepted {
		t.Fatalf("start project %q: %d %s", id, code, body)
	}
}

// StopProject stops a running project. The gateway answers 202: it accepted
// the request, the process departs on its own signal.
func (g *Gateway) StopProject(t *testing.T, id string) {
	t.Helper()
	code, body := g.PostJSON(t, "/api/projects/"+id+"/stop", nil)
	if code != http.StatusOK && code != http.StatusAccepted && code != http.StatusNoContent {
		t.Fatalf("stop project %q: %d %s", id, code, body)
	}
}

// WaitProject waits until the snapshot reports the project with the wanted run
// state, and returns its row.
func (g *Gateway) WaitProject(t *testing.T, id string, running bool) ProjectStatus {
	t.Helper()
	var row ProjectStatus
	WaitFor(t, fmt.Sprintf("project %q running=%v", id, running), 40*time.Second, func() bool {
		got, ok := g.Project(t, id)
		row = got
		return ok && got.Running == running
	})
	return row
}
