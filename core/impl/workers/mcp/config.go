// Package mcp implements the built-in "mcp" worker: a Go worker that proxies
// remote MCP endpoints (Streamable HTTP) onto the niq bus. Configured servers
// are exposed as agentic discovery tools (mcp.server.list / mcp.tool.list /
// mcp.tool.call) and can be managed at runtime (mcp.server.add/remove/update),
// with the dynamic set persisted to the worker's own state.
package mcp

import (
	"fmt"
	"os"
	"strings"
)

// ServerConfig describes one remote MCP endpoint to proxy.
type ServerConfig struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Headers are extra HTTP request headers (e.g. Authorization). Values may
	// reference environment variables as ${VAR}; resolved at parse time. For
	// now stored plaintext (a vault is planned); the key stays out of code.
	Headers map[string]string `json:"headers,omitempty"`
	// Protocol is carried through and shown by mcp.server.list; the client
	// currently always speaks Streamable HTTP.
	Protocol string `json:"protocol,omitempty"`
}

// Expose modes select which extension surface the worker publishes.
const (
	ExposeAgentic = "agentic" // default: mcp.server.list / mcp.tool.list / mcp.tool.call
	ExposeReady   = "ready"   // additionally register one mcp.<server>.<tool> extension per tool
)

// Config is the mcp worker's construction config (from params).
type Config struct {
	Expose string // ExposeAgentic | ExposeReady
	// Fixed makes the worker a config-only tool provider: servers and expose
	// come from params, runtime server mutation and mode switching are not
	// offered, and no runtime state is persisted. This is the "just connect at
	// start and expose fixed tools" mode.
	Fixed   bool
	Servers []ServerConfig
}

// ParseConfig extracts the mcp worker config from worker Params. The config is
// flat — params.expose / params.fixed / params.servers — matching the other
// workers' flat key style. The legacy nested params.mcp.{...} form is still
// accepted for existing projects, with the flat keys winning on a conflict.
// ${VAR} in header values is resolved against the process environment (project
// env is injected into the niq process).
func ParseConfig(params map[string]any) (Config, error) {
	cfg := Config{Expose: ExposeAgentic}
	m := params
	if raw, ok := params["mcp"]; ok && raw != nil {
		nested, ok := raw.(map[string]any)
		if !ok {
			return Config{}, fmt.Errorf("mcp: params.mcp must be an object")
		}
		// Legacy form: start from the nested map, then let the flat keys win.
		m = make(map[string]any, len(nested)+3)
		for k, v := range nested {
			m[k] = v
		}
		for _, k := range []string{"expose", "fixed", "servers"} {
			if v, ok := params[k]; ok {
				m[k] = v
			}
		}
	}
	if e, ok := m["expose"].(string); ok && e != "" {
		cfg.Expose = e
	}
	if f, ok := m["fixed"].(bool); ok {
		cfg.Fixed = f
	}
	if !validExpose(cfg.Expose) {
		return Config{}, fmt.Errorf("mcp: unknown expose %q (want %q or %q)", cfg.Expose, ExposeAgentic, ExposeReady)
	}
	sl, ok := m["servers"].([]any)
	if !ok {
		return cfg, nil
	}
	for i, s := range sl {
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		sc := ServerConfig{}
		sc.Name, _ = sm["name"].(string)
		sc.URL, _ = sm["url"].(string)
		sc.Protocol, _ = sm["protocol"].(string)
		if hm, ok := sm["headers"].(map[string]any); ok {
			sc.Headers = make(map[string]string, len(hm))
			for k, v := range hm {
				sc.Headers[k] = resolveEnv(valueString(v))
			}
		}
		if sc.Name == "" || sc.URL == "" {
			return Config{}, fmt.Errorf("mcp: server[%d] requires non-empty name and url", i)
		}
		cfg.Servers = append(cfg.Servers, sc)
	}
	return cfg, nil
}

// validExpose reports whether s is a known expose mode.
func validExpose(s string) bool { return s == ExposeAgentic || s == ExposeReady }

func valueString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// resolveEnv expands ${VAR} references in a value using the process env.
func resolveEnv(v string) string {
	if !strings.Contains(v, "${") {
		return v
	}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		k := kv[:i]
		v = strings.ReplaceAll(v, "${"+k+"}", kv[i+1:])
	}
	return v
}
