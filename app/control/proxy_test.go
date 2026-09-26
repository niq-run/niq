package control

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/app/project"
)

// setProjectWebUIPort gives an existing project the given WebUI port, as the
// project process would persist it on start.
func setProjectWebUIPort(t *testing.T, id, port string) {
	t.Helper()
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("port %q: %v", port, err)
	}
	p, err := project.LoadProject(id)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	p.Ports.WebUI = n
	if err := project.SaveProject(p); err != nil {
		t.Fatalf("save project: %v", err)
	}
}

// TestProjectProxyUnknownProject asserts an unknown id 404s instead of being
// forwarded anywhere.
func TestProjectProxyUnknownProject(t *testing.T) {
	setupProjectsRoot(t)
	base := newControl(t)

	code, body := doGet(t, base+"/p/nope/")
	if code != http.StatusNotFound {
		t.Fatalf("status=%d (%s), want 404", code, body)
	}
}

// TestProjectProxyNotRunning asserts a project with no assigned WebUI port (i.e.
// not running yet) answers 502 with an explanatory message.
func TestProjectProxyNotRunning(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := project.CreateProject("alpha", "", fakeTemplate()); err != nil {
		t.Fatal(err)
	}
	base := newControl(t)

	code, body := doGet(t, base+"/p/alpha/")
	if code != http.StatusBadGateway {
		t.Fatalf("status=%d (%s), want 502", code, body)
	}
	if !strings.Contains(body, "not running") {
		t.Fatalf("body should explain the project is not running: %s", body)
	}
}

// TestProjectProxyForwards asserts /p/<id>/... reaches the project's own WebUI
// with the mount prefix stripped, the query string intact, and the original
// client address preserved for the upstream.
func TestProjectProxyForwards(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := project.CreateProject("alpha", "", fakeTemplate()); err != nil {
		t.Fatal(err)
	}

	var gotPath, gotQuery, gotXFF string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotXFF = r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Forwarded-For")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	setProjectWebUIPort(t, "alpha", u.Port())

	base := newControl(t)
	code, body := doGet(t, base+"/p/alpha/api/workers?x=1")
	if code != http.StatusOK {
		t.Fatalf("status=%d (%s), want 200", code, body)
	}
	if gotPath != "/api/workers" {
		t.Fatalf("upstream path=%q, want the /p/alpha prefix stripped", gotPath)
	}
	if gotQuery != "x=1" {
		t.Fatalf("upstream query=%q, want x=1", gotQuery)
	}
	if gotXFF == "" {
		t.Fatal("upstream should see X-Forwarded-For from the proxy")
	}
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("body should pass through: %s", body)
	}
}

// TestProjectProxyStreamsSSE asserts the proxy does not buffer: the first chunk
// of a text/event-stream response must reach the client before the upstream
// writes the second one (the project's /api/stream is SSE).
func TestProjectProxyStreamsSSE(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := project.CreateProject("alpha", "", fakeTemplate()); err != nil {
		t.Fatal(err)
	}

	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	defer release()

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		io.WriteString(w, "data: one\n\n")
		if fl != nil {
			fl.Flush()
		}
		<-releaseCh // hold the second chunk until the client proves it saw the first
		io.WriteString(w, "data: two\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	setProjectWebUIPort(t, "alpha", u.Port())

	base := newControl(t)
	resp, err := http.Get(base + "/p/alpha/api/stream")
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type=%q, want text/event-stream", ct)
	}

	br := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if line != "data: one\n" {
			t.Fatalf("first chunk=%q, want %q", line, "data: one\n")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first SSE chunk never reached the client: the proxy is buffering")
	}

	release()
	rest, _ := io.ReadAll(br)
	if !strings.Contains(string(rest), "data: two") {
		t.Fatalf("second chunk missing: %q", rest)
	}
}
