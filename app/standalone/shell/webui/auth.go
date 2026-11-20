package webui

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// AuthConfig is the persisted basic-auth credentials used to protect both the
// control plane and project WebUIs from non-loopback (remote) access. Stored in
// plaintext JSON under <niq root>/common/auth.json with owner-only permissions.
type AuthConfig struct {
	User string `json:"user"`
	Pass string `json:"pass"`
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
// It never blocks startup. enabled is false when no credentials are configured,
// which means the operator opted out of authentication. warning, when non-empty,
// is a user-facing note: it says the server binds a remote-reachable address but
// enforces no auth, so any caller that can reach it is served.
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
		"no basic auth configured: %s is served without authentication, so any caller that can reach it is allowed.\n\n"+
			"To require credentials, create %s with your credentials and restart, e.g.:\n\n"+
			"  { \"user\": \"alice\", \"pass\": \"s3cret\" }\n\n"+
			"or start once with --auth (control) / --webui-auth (project) to generate it for you.",
		addr, path,
	)
	return user, pass, enabled, warning
}

// ParseAuthSpec parses a "user:password" spec into separate parts. A value
// without a colon is treated as the password with a default user of "niq".
// Returns ok=false for an empty/blank spec or when parts would be empty (auth
// stays disabled).
func ParseAuthSpec(spec string) (user, pass string, ok bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", false
	}
	user = "niq"
	pass = spec
	if i := strings.Index(spec, ":"); i >= 0 {
		user, pass = spec[:i], spec[i+1:]
	}
	if user == "" || pass == "" {
		return "", "", false
	}
	return user, pass, true
}

// BasicAuth wraps a handler with HTTP basic auth that applies only to
// non-loopback clients: same-machine (loopback) access stays open, while
// remote peers must present valid credentials. Auth is opt-in: with no
// user/pass configured the handler is a straight pass-through. Works for both
// the project WebUI and the control-plane SPA.
func BasicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authGate(w, r, user, pass) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authGate reports whether the request may proceed; when it may not it has
// already answered 401 with a challenge. Auth is opt-in: with no credentials
// configured the handler is a pass-through (the operator chose not to
// authenticate), and loopback peers are always let through.
func authGate(w http.ResponseWriter, r *http.Request, user, pass string) bool {
	if !authEnabled(user, pass) || isLoopbackRemote(r.RemoteAddr) {
		return true
	}
	gotUser, gotPass, ok := r.BasicAuth()
	if !ok || !secureEqual(gotUser, user) || !secureEqual(gotPass, pass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="niq"`)
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

// isLoopbackRemote reports whether the peer address is a loopback IP
// (127.0.0.0/8 or ::1), i.e. the request originated on this machine.
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authEnabled reports whether basic auth has been configured (both a user and a
// password must be present). Unconfigured means auth is disabled by choice.
func authEnabled(user, pass string) bool {
	return user != "" && pass != ""
}

// secureEqual compares two strings in constant time (via their SHA-256 digests)
// so a password guess cannot be accelerated by measuring comparison time.
func secureEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
