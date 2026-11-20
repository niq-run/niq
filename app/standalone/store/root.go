package store

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Root resolution and path layout for the standalone form.
//
// Everything under the root (projects, shared templates, providers, auth, logs,
// programs) derives from this single point, so a portable install can point
// NIQ_HOME at any directory. Some files predate NIQ_HOME and keep their own
// absolute-file override (NIQ_AUTH_CONFIG, NIQ_PROVIDER_CONFIG); those win over
// NIQ_HOME.
//
// This file is the only place that knows the host layout. Read/write functions
// in this package take a path or a project directory, never a project id.

// Env relocates the niq configuration root.
const Env = "NIQ_HOME"

// Root returns the niq configuration/state root: NIQ_HOME when set (with ~
// expansion), otherwise <home>/.niq.
func Root() string {
	if p := os.Getenv(Env); p != "" {
		return filepath.Clean(expandHome(p))
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".niq")
}

func expandHome(s string) string {
	switch {
	case s == "~":
		home, _ := os.UserHomeDir()
		return home
	case strings.HasPrefix(s, "~/"):
		home, _ := os.UserHomeDir()
		return filepath.Join(home, s[2:])
	default:
		return s
	}
}

// ProjectsRoot returns <root>/projects.
func ProjectsRoot() string { return filepath.Join(Root(), "projects") }

// ProjectDir returns the directory for a project id.
func ProjectDir(id string) string { return filepath.Join(ProjectsRoot(), SanitizeID(id)) }

// ProjectPath returns the project.json path for a project id.
func ProjectPath(id string) string { return filepath.Join(ProjectDir(id), "project.json") }

// ProgramsDir is the project's default program mount: <project>/programs.
func ProgramsDir(projectDir string) string { return filepath.Join(projectDir, "programs") }

// RootSecretPath returns the project's token-signing secret path.
func RootSecretPath(id string) string { return filepath.Join(ProjectDir(id), "root_secret") }

// LogPath is where a project process writes its log. The date in the filename
// gives free daily rotation: a new day starts a new file, same-day restarts
// append.
func LogPath(id string) string {
	return filepath.Join(ProjectDir(id), "logs", "niq-"+time.Now().Format("2006-01-02")+".log")
}

// AuthConfigPath returns the WebUI basic-auth file: <root>/common/auth.json, or
// the NIQ_AUTH_CONFIG override. Its content belongs to the shell; this function
// only says where it lives.
func AuthConfigPath() string {
	if p := os.Getenv("NIQ_AUTH_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Root(), "common", "auth.json")
}

// ProviderConfigPath returns provider.json: <root>/common/providers/provider.json,
// or the NIQ_PROVIDER_CONFIG override.
func ProviderConfigPath() string {
	if p := os.Getenv("NIQ_PROVIDER_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Root(), "common", "providers", "provider.json")
}

// SanitizeID keeps ids path-safe; an id is a directory name, not a path.
func SanitizeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// LocalhostURL turns a resolved listen address (e.g. "[::]:19763") into a
// clickable localhost URL. Exported for the control package, which renders
// project WebUI URLs from resolved addresses.
func LocalhostURL(resolved string) string {
	if resolved == "" {
		return ""
	}
	if _, port, err := net.SplitHostPort(resolved); err == nil {
		return "http://localhost:" + port
	}
	return "http://" + resolved
}

// PortOf parses the numeric port from a host:port address, 0 on failure.
func PortOf(addr string) int {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		if n, err := strconv.Atoi(port); err == nil {
			return n
		}
	}
	return 0
}
