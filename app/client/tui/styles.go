package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Terminal styling for the terminal shell. The CLI has its own copy for its
// own output; duplicating ~40 lines keeps tui self-contained instead of
// depending on the entry package.

var ansi = func() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}()

var truecolor = strings.Contains(os.Getenv("COLORTERM"), "truecolor") ||
	strings.Contains(os.Getenv("COLORTERM"), "24bit") ||
	strings.Contains(os.Getenv("TERM"), "truecolor")

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

func brand(s string) string {
	if !ansi {
		return s
	}
	if truecolor {
		return "\x1b[38;2;60;120;180m" + s + "\x1b[0m"
	}
	return "\x1b[38;5;67m" + s + "\x1b[0m"
}

func brandBold(s string) string {
	if !ansi {
		return s
	}
	if truecolor {
		return "\x1b[1;38;2;60;120;180m" + s + "\x1b[0m"
	}
	return "\x1b[1;38;5;67m" + s + "\x1b[0m"
}

func bracketed(name string) string {
	return brandBold("[") + bold(name) + brandBold("]")
}

func stateWord(running bool) string {
	if running {
		return green(fmt.Sprintf("%-7s", "running"))
	}
	return dim(fmt.Sprintf("%-7s", "stopped"))
}

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

func portCell(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

// displayWidth is how many terminal cells s occupies: an East Asian wide rune
// takes two, everything else one. Project ids are user text (SanitizeID only
// swaps separators), so padding them by byte length would misalign a CJK name
// against an ASCII one.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWide(r) {
			w += 2
			continue
		}
		w++
	}
	return w
}

// isWide reports whether r is East Asian wide or an emoji, the cases a
// terminal renders in two cells.
func isWide(r rune) bool {
	switch {
	case r < 0x1100:
		return false
	case r <= 0x115F, r == 0x2329, r == 0x232A:
		return true
	case r >= 0x2E80 && r <= 0xA4CF: // CJK radicals, kana, han, Yi
		return true
	case r >= 0xAC00 && r <= 0xD7A3: // Hangul syllables
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK compatibility ideographs
		return true
	case r >= 0xFE30 && r <= 0xFE6F: // CJK compatibility forms
		return true
	case r >= 0xFF00 && r <= 0xFF60: // fullwidth forms
		return true
	case r >= 0xFFE0 && r <= 0xFFE6:
		return true
	case r >= 0x1F300 && r <= 0x1FAFF: // emoji and pictographs
		return true
	case r >= 0x20000 && r <= 0x3FFFD:
		return true
	}
	return false
}

// Version is set by the CLI before it opens the picker, so the header shows the
// same version as `niq --version`.
var Version = "dev"

func versionTag() string {
	return dim("version: ") + brand(strings.TrimPrefix(Version, "v"))
}
