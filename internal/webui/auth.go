package webui

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/niq-run/niq/internal/niqhome"
)

// AuthConfig is the persisted basic-auth credentials used to protect both the
// control plane and project WebUIs from non-loopback (remote) access. Stored in
// plaintext JSON under <niq root>/common/auth.json with owner-only permissions.
type AuthConfig struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

// AuthPath returns the on-disk auth file: <niq root>/common/auth.json. The
// NIQ_AUTH_CONFIG environment variable overrides the location wholesale; the
// root itself is set by NIQ_HOME (see internal/niqhome). Useful for tests and
// unusual installs.
func AuthPath() string {
	if p := os.Getenv("NIQ_AUTH_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(niqhome.Root(), "common", "auth.json")
}

// LoadAuth reads the persisted auth config. A missing file yields zero values;
// other errors are surfaced for the caller to handle.
func LoadAuth(path string) (AuthConfig, error) {
	var cfg AuthConfig
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// SaveAuth writes the auth config to path with owner-only permissions (0600),
// creating parent directories as needed.
func SaveAuth(path string, cfg AuthConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// IsLocalOnlyAddr reports whether addr binds exclusively to the loopback
// interface (127.0.0.1 / ::1), i.e. remote hosts cannot reach the server.
// Wildcard and explicit non-loopback addrs (":9527", "0.0.0.0:9527",
// "[::]:9527", "192.168.1.10:9527") are not loopback-only.
func IsLocalOnlyAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// StartupAuth resolves the basic-auth credentials for a WebUI separate of the
// listen address. A non-empty spec ("user:pass", or a bare password defaulting
// the user to "niq") is persisted to the auth file and used immediately;
// otherwise credentials are loaded from the file when present.
//
// It never blocks startup. enabled is false when no credentials are configured.
// warning, when non-empty, is a user-facing note to print: it carries a
// "local-only" notice when the server binds a remote-reachable address but no
// auth is configured (so remote callers would be blocked until auth is set up).
func StartupAuth(addr, spec, path string) (user, pass string, enabled bool, warning string) {
	if strings.TrimSpace(spec) != "" {
		if u, p, ok := ParseAuthSpec(spec); ok {
			if err := SaveAuth(path, AuthConfig{User: u, Pass: p}); err != nil {
				log.Printf("[webui] persist auth to %s: %v", path, err)
			}
			user, pass, enabled = u, p, true
		}
	} else if cfg, err := LoadAuth(path); err == nil && cfg.User != "" && cfg.Pass != "" {
		user, pass, enabled = cfg.User, cfg.Pass, true
	}

	// Loopback-only binding needs no auth and no warning: remote can't reach us.
	// Non-loopback binding with auth configured is fine too.
	if IsLocalOnlyAddr(addr) || enabled {
		return user, pass, enabled, ""
	}

	warning = fmt.Sprintf(
		"no basic auth configured: remote access to %s is disabled, only local access works.\n"+
			"Remote (non-loopback) callers will be blocked until you enable auth.\n\n"+
			"To enable remote access, create %s with your credentials and restart, e.g.:\n\n"+
			"  { \"user\": \"alice\", \"pass\": \"s3cret\" }\n\n"+
			"or start once with --auth (control) / --webui-auth (project) to generate it for you.",
		addr, path,
	)
	return user, pass, enabled, warning
}
