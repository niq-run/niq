//go:build e2e

package cli_e2e

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/e2e/harness"
)

// TestE2EVersionIsTheSameInEverySpelling: scripts and humans ask three ways and
// must get one answer.
func TestE2EVersionIsTheSameInEverySpelling(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	long := s.Run("--version").RequireExit(t, 0).RequireContains(t, "Version:")
	short := s.Run("-v").RequireExit(t, 0)
	word := s.Run("version").RequireExit(t, 0)

	if short.Stdout != long.Stdout || word.Stdout != long.Stdout {
		t.Fatalf("version spellings disagree:\n--version: %q\n-v: %q\nversion: %q",
			long.Stdout, short.Stdout, word.Stdout)
	}
}

// TestE2EHelpListsTheRealCommands guards the help text against drift: it must
// name the commands that exist and not the verbs that were removed.
func TestE2EHelpListsTheRealCommands(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	for _, args := range [][]string{{"--help"}, {"help"}} {
		res := s.Run(args...).RequireExit(t, 0).RequireContains(t,
			"usage",
			"niq status",
			"niq stop",
			"niq restart",
			"niq i",
		)
		if strings.Contains(res.Stdout, "niq project") {
			t.Fatalf("%v still documents the removed `niq project` verb:\n%s", args, res)
		}
	}
}

// TestE2EUnknownVerbFails: a typo is an error with a pointer to help, on stderr.
func TestE2EUnknownVerbFails(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	res := s.Run("frobnicate").RequireExit(t, 1)
	res.RequireStderrContains(t, "error:", "unknown command")
	if strings.Contains(res.Stdout, "error:") {
		t.Fatalf("the error went to stdout:\n%s", res)
	}
}

// TestE2ENoColourWhenPiped: a pipe is not a terminal, so nothing may embed
// escape codes — that is what makes output greppable and these tests possible.
func TestE2ENoColourWhenPiped(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	s.Start(t)

	for _, res := range []harness.Result{
		s.Run("--version"),
		s.Run("status"),
		s.Run("status", "--json"),
	} {
		if strings.Contains(res.Stdout, "\x1b[") {
			t.Fatalf("escape codes in piped output:\n%s", res)
		}
	}
}

// TestE2EStatusJSONIsEmptyArrays: a client decodes a list, so "no projects" is
// [] and not null.
func TestE2EStatusJSONIsEmptyArrays(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	res := s.Run("status", "--json").RequireExit(t, 0)
	if !strings.Contains(res.Stdout, `"projects": []`) {
		t.Fatalf("empty project list is not []:\n%s", res)
	}
}
