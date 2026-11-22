package contract

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recorder is a gateway stub: it captures the request the client sent and
// answers with a fixed status and body, so the wire contract can be asserted
// without a real gateway.
type recorder struct {
	method string
	path   string
	status int
	body   string
}

// call runs one client operation against a fresh stub and returns its error.
func (r *recorder) call(t *testing.T, op func(*Client) error) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.method, r.path = req.Method, req.URL.EscapedPath()
		if r.status != 0 {
			w.WriteHeader(r.status)
		}
		if r.body != "" {
			io.WriteString(w, r.body)
		}
	}))
	t.Cleanup(srv.Close)
	return op(New(srv.URL))
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	if got := New("http://127.0.0.1:9527/").BaseURL; got != "http://127.0.0.1:9527" {
		t.Fatalf("BaseURL = %q", got)
	}
}

// TestListDecodesViews pins the response the picker renders from.
func TestListDecodesViews(t *testing.T) {
	r := &recorder{body: `[{"id":"demo","running":true,"dir":"/tmp/demo","ports":{"webui":1234,"bus":5678}}]`}
	var views []View
	if err := r.call(t, func(c *Client) error {
		got, err := c.List()
		views = got
		return err
	}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if r.method != http.MethodGet || r.path != "/api/projects" {
		t.Fatalf("request = %s %s", r.method, r.path)
	}
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	v := views[0]
	if v.ID != "demo" || !v.Running || v.Dir != "/tmp/demo" || v.Ports.WebUI != 1234 || v.Ports.Bus != 5678 {
		t.Fatalf("view = %+v", v)
	}
}

// TestLifecyclePaths pins method and path for the three actions the picker
// offers; these are the only writes a UI client performs.
func TestLifecyclePaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   func(*Client) error
		path string
	}{
		{"start", func(c *Client) error { _, err := c.Start("demo"); return err }, "/api/projects/demo/start"},
		{"restart", func(c *Client) error { _, err := c.Restart("demo"); return err }, "/api/projects/demo/restart"},
		{"stop", func(c *Client) error { return c.Stop("demo") }, "/api/projects/demo/stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{body: `{"project":"demo","webui_port":1234}`}
			if err := r.call(t, tc.op); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if r.method != http.MethodPost || r.path != tc.path {
				t.Fatalf("request = %s %s, want POST %s", r.method, r.path, tc.path)
			}
		})
	}
}

// TestStartReturnsInstance checks the decoded reply (the picker only reads
// Already, but the shape is the contract).
func TestStartReturnsInstance(t *testing.T) {
	r := &recorder{body: `{"project":"demo","webui_port":1234,"bus_port":5678,"already":true}`}
	var inst Instance
	if err := r.call(t, func(c *Client) error {
		got, err := c.Start("demo")
		inst = got
		return err
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst.ID != "demo" || inst.WebUIPort != 1234 || inst.BusPort != 5678 || !inst.Already {
		t.Fatalf("instance = %+v", inst)
	}
}

// TestEmptyBodySucceeds covers 204/empty replies: Stop decodes nothing, so an
// empty body must not read as a decode failure.
func TestEmptyBodySucceeds(t *testing.T) {
	r := &recorder{status: http.StatusNoContent}
	if err := r.call(t, func(c *Client) error { return c.Stop("demo") }); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestErrorCarriesTheServerMessage keeps the gateway's explanation intact: the
// picker prints err.Error() as its status line, and "project is not running"
// must not become "502 Bad Gateway".
func TestErrorCarriesTheServerMessage(t *testing.T) {
	r := &recorder{status: http.StatusBadRequest, body: "project demo is not running\n"}
	err := r.call(t, func(c *Client) error { return c.Stop("demo") })
	if err == nil || err.Error() != "project demo is not running" {
		t.Fatalf("err = %v", err)
	}
}

// TestErrorFallsBackToStatus covers a refusal with no body at all: the message
// is then the status line the transport reported.
func TestErrorFallsBackToStatus(t *testing.T) {
	r := &recorder{status: http.StatusBadGateway}
	err := r.call(t, func(c *Client) error { return c.Stop("demo") })
	if err == nil || err.Error() != "502 Bad Gateway" {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeErrorNamesTheGateway(t *testing.T) {
	r := &recorder{body: "not json"}
	err := r.call(t, func(c *Client) error {
		_, err := c.List()
		return err
	})
	if err == nil {
		t.Fatal("decoding a non-JSON body succeeded")
	}
}

// TestIDIsEscaped keeps a project id out of the path grammar.
func TestIDIsEscaped(t *testing.T) {
	r := &recorder{}
	if err := r.call(t, func(c *Client) error { return c.Stop("a/b") }); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if r.path != "/api/projects/a%2Fb/stop" {
		t.Fatalf("path = %q", r.path)
	}
}

// TestProjectURL is where every UI link comes from: the shell lives under
// /p/<id>/, never on the project's own loopback port.
func TestProjectURL(t *testing.T) {
	for _, tc := range []struct{ base, id, want string }{
		{"http://localhost:9527", "demo", "http://localhost:9527/p/demo/"},
		{"http://localhost:9527/", "demo", "http://localhost:9527/p/demo/"},
		{"http://localhost:9527", "a b", "http://localhost:9527/p/a%20b/"},
	} {
		if got := ProjectURL(tc.base, tc.id); got != tc.want {
			t.Errorf("ProjectURL(%q, %q) = %q, want %q", tc.base, tc.id, got, tc.want)
		}
	}
}
