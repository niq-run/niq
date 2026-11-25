package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/contract"
)

// TestUploadsFiles pins the locality decision: a loopback gateway shares this
// disk (reference by path), any other address might not (upload), and the
// NIQ_ATTACH override wins either way.
func TestUploadsFiles(t *testing.T) {
	t.Setenv("NIQ_ATTACH", "")
	hostname, _ := os.Hostname()

	cases := []struct {
		url  string
		want bool
	}{
		{"http://127.0.0.1:9527", false},
		{"http://localhost:9527", false},
		{"http://[::1]:9527", false},
		{"https://niq.example.com", true},
		{"http://10.0.0.5:9527", true},
	}
	if hostname != "" {
		cases = append(cases, struct {
			url  string
			want bool
		}{"http://" + hostname + ":9527", false})
	}
	for _, c := range cases {
		if got := uploadsFiles(c.url); got != c.want {
			t.Errorf("uploadsFiles(%q) = %v, want %v", c.url, got, c.want)
		}
	}

	t.Setenv("NIQ_ATTACH", "upload")
	if !uploadsFiles("http://127.0.0.1:9527") {
		t.Error("NIQ_ATTACH=upload did not force uploads")
	}
	t.Setenv("NIQ_ATTACH", "path")
	if uploadsFiles("https://niq.example.com") {
		t.Error("NIQ_ATTACH=path did not force path references")
	}
}

// TestSendUploadsRemoteFiles checks that a remote gateway receives the file and
// that the message references the path the gateway returned, while a local one
// keeps the terminal's own path.
func TestSendUploadsRemoteFiles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(src, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	var uploadedPath string
	var sent contract.Input
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/upload"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse upload: %v", err)
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			f, hdr, err := r.FormFile("file")
			if err != nil {
				t.Errorf("form file: %v", err)
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			defer f.Close()
			uploadedPath = "/remote/uploads/" + hdr.Filename
			json.NewEncoder(w).Encode(map[string]any{
				"path": uploadedPath, "name": hdr.Filename, "size": 4,
			})
		case strings.HasSuffix(r.URL.Path, "/api/input"):
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decode input: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	in := newInput()
	in.ed.setText("see attached")
	in.attach = []attachment{{kind: "file", name: "notes.txt", path: src, size: 4}}

	remote := &chat{client: contract.New(srv.URL), projectID: "p1", uploadFiles: true}
	if err := remote.send(context.Background(), in, "niq"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if uploadedPath == "" {
		t.Fatal("file was not uploaded")
	}
	if len(sent.Content) != 2 || sent.Content[1].Type != "file" || sent.Content[1].Path != uploadedPath {
		t.Fatalf("sent content = %+v", sent.Content)
	}
	if sent.Content[1].Name != "notes.txt" || sent.Content[1].Size != 4 {
		t.Fatalf("file block = %+v", sent.Content[1])
	}

	// A local gateway keeps the original path and uploads nothing.
	uploadedPath = ""
	sent = contract.Input{}
	local := &chat{client: contract.New(srv.URL), projectID: "p1", uploadFiles: false}
	if err := local.send(context.Background(), in, "niq"); err != nil {
		t.Fatalf("local send: %v", err)
	}
	if uploadedPath != "" {
		t.Fatal("local send uploaded the file")
	}
	if len(sent.Content) != 2 || sent.Content[1].Path != src {
		t.Fatalf("local content = %+v", sent.Content)
	}
}
