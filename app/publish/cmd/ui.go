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

// truecolor reports whether the terminal advertises 24-bit colour; without it
// the brand colour falls back to the nearest 256-colour entry.
var truecolor = strings.Contains(os.Getenv("COLORTERM"), "truecolor") ||
	strings.Contains(os.Getenv("COLORTERM"), "24bit") ||
	strings.Contains(os.Getenv("TERM"), "truecolor")

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

// brand is the product's theme colour, #3C78B4 — the square of the web UI logo.
// Anything that is "niq" rather than a state uses it.
func brand(s string) string {
	if !ansi {
		return s
	}
	if truecolor {
		return "\x1b[38;2;60;120;180m" + s + "\x1b[0m"
	}
	return "\x1b[38;5;67m" + s + "\x1b[0m"
}

// logoChip is the niq mark on one line: the wordmark knocked out of the
// product's blue, the same mark the web UI ships, sized to sit inline with the
// version. Empty when colour is off — a banner made of spaces helps nobody.
func logoChip() string {
	if !ansi {
		return ""
	}
	bg, fg := "\x1b[48;2;60;120;180m", "\x1b[1;38;2;238;238;238m"
	if !truecolor {
		bg, fg = "\x1b[48;5;67m", "\x1b[1;38;5;255m"
	}
	// 8 columns wide so the version starts in the same column as the values
	// under it ("running", the URL, the commands) — the chip reads as the label
	// of the first row instead of hanging short of the table.
	return bg + fg + "  niq   " + "\x1b[0m"
}

// logoWidth is the chip's width in columns.
const logoWidth = 8

// versionNumber is the build version without git describe's leading "v", so the
// banner can read "version-0.13.0-…" instead of "version-v0.13.0-…".
func versionNumber() string { return strings.TrimPrefix(version, "v") }

// banner is the identity block printed beside the mark: version, an optional
// one-liner, and the state. The mark already carries the product name, so the
// text does not repeat it.
type banner struct {
	version string
	tagline string
	state   string
}

// printBanner prints the mark inline with the version, then the tagline and the
// state on their own lines. Without colour there is no mark to carry the name,
// so the first line leads with it instead.
func printBanner(b banner) {
	if chip := logoChip(); chip != "" {
		fmt.Printf("  %s %s\n", chip, b.version)
	} else {
		// Same column as the chip, so the report lines up with or without colour.
		fmt.Printf("  %s%s %s\n", bold("niq"), strings.Repeat(" ", logoWidth-len("niq")), b.version)
	}
	if b.tagline != "" {
		fmt.Printf("  %s\n", b.tagline)
	}
	if b.state != "" {
		fmt.Printf("\n  %s\n", b.state)
	}
}

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
	return bold("niq") + "  " + dim("version-") + brand(versionNumber())
}
