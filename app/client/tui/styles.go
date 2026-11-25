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

var color256 = truecolor || strings.Contains(os.Getenv("TERM"), "256color")

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

// Conversation and markdown styles. underline marks the human's own messages so
// they stand out in the transcript; italic, underline again for links, and a
// distinct colour for inline/block code keep a formatted answer legible without
// the raw markers.
func underline(s string) string { return paint("4", s) }
func italic(s string) string    { return paint("3", s) }
func codeText(s string) string  { return paint("36", s) }

// reverse paints s as a filled block: the terminal swaps foreground and
// background (a selection highlight), leaving the surrounding style intact.
func reverse(s string) string {
	if !ansi {
		return s
	}
	return "\x1b[7m" + s + "\x1b[27m"
}

// gray is a muted foreground for thinking: readable but clearly secondary, and
// not a colour that competes with the transcript.
func gray(s string) string     { return paint("90", s) }
func grayBold(s string) string { return paint("1;90", s) }

// warnText is the body tone for a warning row on the amber surface: a soft warm
// grey, since plain dim would sink into the band.
func warnText(s string) string {
	if !ansi {
		return s
	}
	if truecolor {
		if lightTheme {
			return "\x1b[38;2;92;76;52m" + s + "\x1b[0m"
		}
		return "\x1b[38;2;214;196;162m" + s + "\x1b[0m"
	}
	if lightTheme {
		return paint("38;5;240", s)
	}
	return paint("38;5;180", s)
}

// Syntax-highlighting colours for fenced code. They are deliberately plain ANSI
// shades rather than theme-aware, so they read the same on light and dark
// terminals: keywords bold cyan, strings green, numbers yellow, comments dim.
func codeKeyword(s string) string { return paint("1;36", s) }
func codeString(s string) string  { return paint("32", s) }
func codeNumber(s string) string  { return paint("33", s) }
func codeComment(s string) string { return paint("2", s) }

// Speaker and event-title colours. They give each kind of line its own hue so a
// transcript can be scanned without reading every label: the human is green, a
// worker's answer keeps the brand blue, thinking is magenta, ordinary events are
// cyan, warnings yellow and failures red.
func greenBold(s string) string    { return paint("1;32", s) }
func magentaBold(s string) string  { return paint("1;35", s) }
func cyanBold(s string) string     { return paint("1;36", s) }
func yellowBright(s string) string { return paint("1;93", s) }
func redBold(s string) string      { return paint("1;31", s) }

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

// brandBg is the brand accent as a background. It paints the frame the modals
// sit in, giving a popup the blue outline classic terminal programs use.
func brandBg() string {
	if !ansi {
		return ""
	}
	if truecolor {
		return "\x1b[48;2;60;120;180m"
	}
	if color256 {
		return "\x1b[48;5;67m"
	}
	return "\x1b[44m"
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

// Version is set by the CLI before it opens the picker, so the header shows the
// same version as `niq --version`.
var Version = "dev"

func versionTag() string {
	return dim("version: ") + brand(strings.TrimPrefix(Version, "v"))
}
