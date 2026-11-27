package gateway

import (
	"encoding/json"
	"io"
	stdhttp "net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/store"
)

// newControlWithHome isolates ~/.niq under a temp HOME and starts a control
// plane, so the vault it manages lives in the temp tree.
func newControlWithHome(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	_ = os.Unsetenv(store.Env)
	return newControl(t)
}

func doJSON(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, err := stdhttp.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := stdhttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestVaultAPI: the management API can write, list and delete, and the list never
// returns a value.
func TestVaultAPI(t *testing.T) {
	base := newControlWithHome(t)

	code, body := doJSON(t, "PUT", base+"/api/vault/openai%2Fmain", `{"value":"sk-s3cret"}`)
	if code != 200 {
		t.Fatalf("put status=%d (%s)", code, body)
	}

	// On disk: one file per entry, mode 0600, under common/vault.
	path := filepath.Join(store.VaultDir(), "openai", "main")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("entry file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "sk-s3cret") {
		t.Errorf("file content = %q", raw)
	}

	// List: metadata only, never the secret.
	code, body = doGet(t, base+"/api/vault")
	if code != 200 {
		t.Fatalf("list status=%d", code)
	}
	if !strings.Contains(body, "openai/main") {
		t.Errorf("list = %s, want the entry", body)
	}
	if strings.Contains(body, "sk-s3cret") {
		t.Fatalf("list leaked the value: %s", body)
	}
	var parsed struct {
		Entries []vaultEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("list json: %v", err)
	}
	if len(parsed.Entries) != 1 || parsed.Entries[0].Size == 0 {
		t.Fatalf("entries = %+v", parsed.Entries)
	}

	// Delete, then the same call reports not-found.
	if code, body := doJSON(t, "DELETE", base+"/api/vault/openai%2Fmain", ""); code != 204 {
		t.Fatalf("delete status=%d (%s)", code, body)
	}
	if code, _ := doJSON(t, "DELETE", base+"/api/vault/openai%2Fmain", ""); code != 404 {
		t.Fatalf("second delete status=%d, want 404", code)
	}
}

func TestVaultAPIRejectsBadNamesAndEmptyValues(t *testing.T) {
	base := newControlWithHome(t)

	// A traversal attempt is a name error, not a write outside the root.
	bad := url.PathEscape("../escape")
	if code, body := doJSON(t, "PUT", base+"/api/vault/"+bad, `{"value":"x"}`); code != 400 {
		t.Fatalf("traversal status=%d (%s), want 400", code, body)
	}
	if code, _ := doJSON(t, "PUT", base+"/api/vault/ok", `{"value":""}`); code != 400 {
		t.Fatalf("empty value status=%d, want 400", code)
	}
}
