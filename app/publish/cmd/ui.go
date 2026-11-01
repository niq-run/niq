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
func red(s string) string   { return paint("31", s) }

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

// brandBold is brand() in bold, for glyphs that have to carry the theme colour
// and the weight at once (nesting paint() would end the bold at the inner
// reset).
func brandBold(s string) string {
	if !ansi {
		return s
	}
	if truecolor {
		return "\x1b[1;38;2;60;120;180m" + s + "\x1b[0m"
	}
	return "\x1b[1;38;5;67m" + s + "\x1b[0m"
}

// logoChip is the niq mark on one line: the wordmark knocked out of the
// product's blue, the same mark the web UI ships, sized to sit inline with the
// version. Empty when colour is off — a banner made of spaces helps nobody.
func logoChip() string {
	if !ansi {
		return ""
	}
	bg, fg, rule := "\x1b[48;2;60;120;180m", "\x1b[38;2;238;238;238m", "\x1b[38;2;169;205;235m"
	if !truecolor {
		bg, fg, rule = "\x1b[48;5;67m", "\x1b[38;5;255m", "\x1b[38;5;153m"
	}
	// A rule either side of the wordmark, with a little air between each rule
	// and the box edge. Nothing resets until the end, so the background covers
	// the whole box.
	return bg + rule + " - " + fg + "niq" + rule + " - " + "\x1b[0m"
}

// versionNumber is the build version without git describe's leading "v", so the
// banner can read "version-0.13.0-…" instead of "version-v0.13.0-…".
func versionNumber() string { return strings.TrimPrefix(version, "v") }

// banner is the identity block printed under the mark: an optional one-liner
// and the state. The mark already carries the product name, so the text does
// not repeat it.
type banner struct {
	tagline string
	state   string
}

// printBanner prints the mark with the version, then the tagline and the state
// on their own lines.
func printBanner(b banner) {
	fmt.Println(versionTag())
	if b.tagline != "" {
		fmt.Printf("%s\n", b.tagline)
	}
	if b.state != "" {
		fmt.Printf("\n%s\n", b.state)
	}
}

// bracketed renders a name inside dim square brackets: the product uses them to
// mark "this is a thing you can act on" in states, handlers and project rows.
func bracketed(name string) string {
	return brandBold("[") + bold(name) + brandBold("]")
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
// start banner, --version, --help and the picker all lead with it. It is the
// mark followed by the version, and without colour (no mark) the name stands in
// for the mark.
func versionTag() string {
	tag := dim("version-") + brand(versionNumber())
	if chip := logoChip(); chip != "" {
		return chip + " " + tag
	}
	return bold("niq") + "  " + tag
}
