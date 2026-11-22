//go:build e2e

package cli_e2e

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/e2e/harness"
)

// TestE2ECompletionScriptsAreCurrent checks what a completion script must get
// right: it exists, it is syntactically valid for its shell, and it offers the
// commands the CLI actually has.
func TestE2ECompletionScriptsAreCurrent(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	for _, shell := range []string{"bash", "zsh", "fish"} {
		res := s.Run("completion", shell).RequireExit(t, 0)
		if len(res.Stdout) < 200 {
			t.Fatalf("%s completion is suspiciously short (%d bytes)", shell, len(res.Stdout))
		}
		if strings.Contains(res.Stdout, "--webui-auth") || strings.Contains(res.Stdout, "project run") {
			t.Fatalf("%s completion still offers removed flags:\n%s", shell, res.Stdout)
		}
		if !strings.Contains(res.Stdout, "niq") {
			t.Fatalf("%s completion never mentions niq", shell)
		}
	}
}

// TestE2ECompletionScriptsParse runs each script through its own parser. A
// completion that does not parse breaks the shell it was installed into.
func TestE2ECompletionScriptsParse(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	for _, tc := range []struct{ shell, parser string }{
		{"bash", "bash"},
		{"zsh", "zsh"},
	} {
		binary, err := exec.LookPath(tc.parser)
		if err != nil {
			t.Logf("%s not installed; skipping its parse check", tc.parser)
			continue
		}
		script := s.Run("completion", tc.shell).RequireExit(t, 0).Stdout

		cmd := exec.Command(binary, "-n")
		cmd.Stdin = strings.NewReader(script)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s -n rejected the generated %s completion: %v\n%s", tc.parser, tc.shell, err, stderr.String())
		}
	}
}

// TestE2ECompletionInstallDryRunWritesNothing: the path is printed so a user
// can place it themselves, and nothing on disk changes.
func TestE2ECompletionInstallDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	for _, shell := range []string{"bash", "fish"} {
		res := s.Run("completion", "install", "--dry-run", "--shell", shell).RequireExit(t, 0)
		path := strings.TrimSpace(res.Stdout)
		if !filepath.IsAbs(path) {
			t.Fatalf("%s dry-run printed %q, want an absolute path", shell, path)
		}
		if fileExists(path) {
			t.Fatalf("%s dry-run wrote %s", shell, path)
		}
	}
}

// TestE2ECompletionInstallIsIdempotent: installing twice replaces the file, so
// an upgrade does not need the user to clean up first.
func TestE2ECompletionInstallIsIdempotent(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	script := s.Run("completion", "bash").RequireExit(t, 0).Stdout

	s.Run("completion", "install", "--shell", "bash").RequireExit(t, 0)
	path := filepath.Join(s.Dir, ".local", "share", "bash-completion", "completions", "niq")
	installed, ok := readPath(path)
	if !ok {
		t.Fatalf("nothing was installed at %s", path)
	}
	if installed != script {
		t.Fatal("the installed script is not the generated one")
	}

	s.Run("completion", "install", "--shell", "bash").RequireExit(t, 0)
	again, ok := readPath(path)
	if !ok || again != script {
		t.Fatal("re-installing changed the file")
	}
}

// TestE2ECompletionInstallFishUsesXDG: fish finds completions under
// $XDG_CONFIG_HOME, which is what a sandbox sets.
func TestE2ECompletionInstallFishUsesXDG(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	s.Run("completion", "install", "--shell", "fish").RequireExit(t, 0)
	path := filepath.Join(s.Dir, ".config", "fish", "completions", "niq.fish")
	if _, ok := readPath(path); !ok {
		t.Fatalf("nothing was installed at %s", path)
	}
}

// TestE2ECompletionRejectsUnknownInput keeps the failure loud.
func TestE2ECompletionRejectsUnknownInput(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	s.Run("completion", "powershell").RequireExit(t, 1).RequireStderrContains(t, "unknown target")
	s.Run("completion", "install", "--shell", "tcsh").RequireExit(t, 1)
}
