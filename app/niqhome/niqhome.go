// Package niqhome resolves where niq's configuration/state root lives.
//
// The root is normally ~/.niq, but can be relocated wholesale by setting the
// NIQ_HOME environment variable. Everything under the root (projects, shared
// templates, providers, auth, logs, programs) is derived from this single
// resolution point so a "portable" niq install can point NIQ_HOME at any
// directory.
//
// Some individual files predate NIQ_HOME and already have their own dedicated
// override (NIQ_AUTH_CONFIG, NIQ_PROVIDER_CONFIG). Those are absolute-file
// overrides and take priority over NIQ_HOME; NIQ_HOME only changes the default.
package niqhome

import (
	"os"
	"path/filepath"
	"strings"
)

// Env is the environment variable that relocates the niq config root.
const Env = "NIQ_HOME"

// Root returns the niq configuration/state root directory.
//
// Resolution order:
//
//  1. NIQ_HOME, if set ("~" / "~/" are expanded to the home directory)
//  2. <home>/.niq
func Root() string {
	if p := os.Getenv(Env); p != "" {
		return filepath.Clean(expandHome(p))
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".niq")
}

// expandHome expands a leading ~ or ~/ in s to the user's home directory.
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
