//go:build e2e

package worker_e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// mountList decodes the snapshot the mounts endpoints answer with.
type mountList struct {
	Done   bool     `json:"done"`
	Mounts []string `json:"mounts"`
	Path   string   `json:"path"`
}

func decodeMounts(t *testing.T, body string) mountList {
	t.Helper()
	var out mountList
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode mounts: %v\n%s", err, body)
	}
	return out
}

func hasMount(list mountList, want string) bool {
	for _, m := range list.Mounts {
		if m == want {
			return true
		}
	}
	return false
}

// TestE2EWorkspaceMountsAreEditable is the workspace's boundary under the
// user's control: the mount list is readable, a mount can be added and removed,
// and the answer always carries the fresh snapshot.
//
// The default template already declares one workspace ("ws") over the project's
// workspace, programs and uploads directories, so the test edits that one
// rather than inventing a second.
func TestE2EWorkspaceMountsAreEditable(t *testing.T) {
	t.Parallel()
	s, g, project := newProject(t)

	code, body := g.Get(t, "/p/"+project+"/api/workers/ws/mounts")
	if code != http.StatusOK {
		t.Fatalf("list mounts: %d %s", code, body)
	}
	initial := decodeMounts(t, body)
	if len(initial.Mounts) != 3 {
		t.Fatalf("the template declares three mounts, got %d: %s", len(initial.Mounts), body)
	}
	if !hasMount(initial, initial.Mounts[0]) || !strings.Contains(initial.Mounts[0], "workspace") {
		t.Fatalf("the primary mount is not the project workspace: %s", body)
	}

	extra := mountPath(s, "extra")
	code, body = g.PostJSON(t, "/p/"+project+"/api/workers/ws/mounts/add", map[string]any{"path": extra})
	if code != http.StatusOK {
		t.Fatalf("add mount: %d %s", code, body)
	}
	if !hasMount(decodeMounts(t, body), extra) {
		t.Fatalf("the added mount is missing from the snapshot: %s", body)
	}

	code, body = g.PostJSON(t, "/p/"+project+"/api/workers/ws/mounts/remove", map[string]any{"path": extra})
	if code != http.StatusOK {
		t.Fatalf("remove mount: %d %s", code, body)
	}
	removed := decodeMounts(t, body)
	if hasMount(removed, extra) {
		t.Fatalf("the removed mount is still listed: %s", body)
	}
	if len(removed.Mounts) != 3 {
		t.Fatalf("removing one mount left %d, want the template's three: %s", len(removed.Mounts), body)
	}
}

// TestE2EWorkspaceRequiresAMount: the boundary is the point of the worker, so
// declaring one with no mount must be refused rather than silently useless.
func TestE2EWorkspaceRequiresAMount(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)

	code, body := g.PostJSON(t, "/p/"+project+"/api/workers/create", map[string]any{
		"id":        "ws-empty",
		"type":      "workspace",
		"placement": "in-process",
		"params":    map[string]any{},
	})
	if code == http.StatusOK {
		t.Fatalf("a workspace without mounts was created: %s", body)
	}
	if !strings.Contains(body, "mount") {
		t.Fatalf("the refusal does not mention mounts: %s", body)
	}
}

// TestE2EMountsAreRefusedForANonWorkspace keeps the endpoint honest: mounts are
// a workspace concept, not a generic worker setting.
func TestE2EMountsAreRefusedForANonWorkspace(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)

	code, body := g.Get(t, "/p/"+project+"/api/workers/timer/mounts")
	if code == http.StatusOK {
		t.Fatalf("the timer worker answered a mounts query: %s", body)
	}
}
