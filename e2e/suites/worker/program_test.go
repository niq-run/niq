//go:build e2e

package worker_e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/niq-run/niq/e2e/harness"
)

// createProgram touches a program, which is how the program worker creates one:
// metadata only, idempotent, and the step the editor endpoint expects first.
func createProgram(t *testing.T, g *harness.Gateway, project, name, contentType string) {
	t.Helper()
	code, body := g.PostJSON(t, "/p/"+project+"/api/workers/program/event", map[string]any{
		"type": "touch",
		"payload": map[string]any{
			"name":         name,
			"content_type": contentType,
		},
	})
	if code != http.StatusOK {
		t.Fatalf("touch program %q: %d %s", name, code, body)
	}
}

// TestE2EProgramsAreWrittenAndReadBack covers the program store end to end: a
// program is created, its body written through the editor endpoint, read back
// through the content endpoint, and listed.
func TestE2EProgramsAreWrittenAndReadBack(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)

	// The list is a JSON array even when the store is empty.
	if code, body := g.Get(t, "/p/"+project+"/api/programs"); code != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(body), "[") {
		t.Fatalf("list programs: %d %s", code, body)
	}

	createProgram(t, g, project, "e2e-prog", "instruction")

	code, body := g.PutJSON(t, "/p/"+project+"/api/programs/e2e-prog", map[string]any{
		"content_type": "instruction",
		"description":  "written by the e2e suite",
		"body":         "hello from e2e",
	})
	if code != http.StatusOK {
		t.Fatalf("write program: %d %s", code, body)
	}

	code, body = g.Get(t, "/p/"+project+"/api/programs/e2e-prog/content?path=e2e-prog")
	if code != http.StatusOK {
		t.Fatalf("read program content: %d %s", code, body)
	}
	if !strings.Contains(body, "hello from e2e") {
		t.Fatalf("the body did not survive the round trip: %s", body)
	}

	if code, body := g.Get(t, "/p/"+project+"/api/programs"); code != http.StatusOK || !strings.Contains(body, "e2e-prog") {
		t.Fatalf("the new program is not listed: %d %s", code, body)
	}
}

// TestE2EProgramDeleteRemovesIt: delete has to be visible to the next reader,
// not just acknowledged.
func TestE2EProgramDeleteRemovesIt(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)

	createProgram(t, g, project, "e2e-doomed", "instruction")
	if code, body := g.PutJSON(t, "/p/"+project+"/api/programs/e2e-doomed", map[string]any{
		"content_type": "instruction",
		"description":  "short lived",
		"body":         "bye",
	}); code != http.StatusOK {
		t.Fatalf("write program: %d %s", code, body)
	}

	code, body := g.Delete(t, "/p/"+project+"/api/programs/e2e-doomed")
	if code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("delete program: %d %s", code, body)
	}

	code, body = g.Get(t, "/p/"+project+"/api/programs/e2e-doomed/content?path=e2e-doomed")
	if code == http.StatusOK {
		t.Fatalf("the deleted program still reads back: %s", body)
	}
}
