package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to a niq gateway's project endpoints. It holds no session: a
// caller can point one at any instance, local or remote, and the two fields are
// just the base URL and the transport it uses.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a client for baseURL (e.g. "http://127.0.0.1:9527").
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// ProjectURL is where a project's interface is reachable on a gateway: the
// shell lives under /p/<id>/, and the project's own port is a data port that
// speaks JSON and SSE only. Callers build this instead of pasting the path.
func ProjectURL(baseURL, id string) string {
	return strings.TrimRight(baseURL, "/") + "/p/" + url.PathEscape(id) + "/"
}

// List returns every project with its running state.
func (c *Client) List() ([]View, error) {
	var out []View
	if err := c.do("GET", "/api/projects", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Start brings a project up and returns the resolved instance.
func (c *Client) Start(id string) (Instance, error) { return c.instance("start", id) }

// Restart stops a project and starts it again on the same ports.
func (c *Client) Restart(id string) (Instance, error) { return c.instance("restart", id) }

// Stop terminates a running project. A project that is not running is an
// error from the gateway, not a silent success.
func (c *Client) Stop(id string) error {
	return c.do("POST", "/api/projects/"+url.PathEscape(id)+"/stop", nil, nil)
}

func (c *Client) instance(action, id string) (Instance, error) {
	var inst Instance
	path := "/api/projects/" + url.PathEscape(id) + "/" + action
	if err := c.do("POST", path, nil, &inst); err != nil {
		return Instance{}, err
	}
	return inst, nil
}

func (c *Client) do(method, path string, body io.Reader, out any) error {
	var buf io.Reader
	if body != nil {
		buf = body
	}
	req, err := http.NewRequest(method, c.BaseURL+path, buf)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("gateway %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("%s", msg)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gateway %s: decode response: %w", c.BaseURL, err)
	}
	return nil
}
