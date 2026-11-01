// Terminal presentation for the CLI: colors and the few shapes every command
// shares (state dots, padded rows, shortened paths).
//
// Colors are emitted only when stdout is a terminal and the environment has not
// opted out, so piped output, completion scripts and logs stay plain.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

var ansi = colorEnabled()

func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func paint(code, s string) string {
	if !ansi {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string  { return paint("1", s) }
func dim(s string) string   { return paint("2", s) }
func green(s string) string { return paint("32", s) }
func cyan(s string) string  { return paint("36", s) }

// stateDot marks a running/stopped instance. It is the same glyph in the status
// report and in the picker, so "am I looking at the same thing?" never costs a
// thought.
func stateDot(running bool) string {
	if running {
		return green("●")
	}
	return dim("○")
}

// stateWord renders the fixed-width state cell. Padding is applied before the
// color codes so alignment survives them.
func stateWord(running bool) string {
	if running {
		return green(fmt.Sprintf("%-7s", "running"))
	}
	return dim(fmt.Sprintf("%-7s", "stopped"))
}

// label pads a field label before coloring it: color codes would otherwise
// count towards the column width and collapse the alignment.
func label(s string, width int) string {
	return dim(fmt.Sprintf("%-*s", width, s))
}

// shortPath abbreviates the home directory, which is where every niq path lives.
func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// shortTime renders a recorded RFC3339 timestamp in local time, falling back to
// the raw value when it does not parse (an older record, a hand-edited file).
func shortTime(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.Local().Format("2006-01-02 15:04")
}

func portCell(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

// versionTag is the one place the CLI prints its identity: `niq status`, the
// start banner, the picker and --help all lead with it.
func versionTag() string {
	return bold("niq") + " " + cyan(version)
}
