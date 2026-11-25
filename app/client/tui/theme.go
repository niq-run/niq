// Background bands and rules, in the spirit of pi's theme: the transcript reads
// as layers because the human's turns, tool rows and code blocks sit on their own
// subtle surfaces, while prose stays on the terminal's background.
//
// pi derives a full palette from the terminal's reported background (OSC 11) and
// falls back by lightness. We keep that shape but not the machinery: two fixed
// palettes (dark and light), chosen from COLORFGBG when it is present, and
// downgraded to 256 colours or no band at all (NO_COLOR / a 16-colour terminal).
package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// bandKind names the surface a block is painted on. bandNone leaves the
// terminal's own background.
type bandKind int

const (
	bandNone bandKind = iota
	bandUser
	bandTool
	bandCode
	bandError
	bandDock
)

// shade is one surface: an RGB triplet for truecolor and an ANSI-256 index for
// terminals that only do 256.
type shade struct {
	r, g, b int
	idx     int
}

type palette struct {
	user, tool, code, dock, err shade
}

var darkPalette = palette{
	user: shade{46, 58, 76, 237},
	tool: shade{20, 44, 32, 22},
	code: shade{26, 29, 35, 234},
	dock: shade{24, 26, 31, 234},
	err:  shade{58, 34, 38, 52},
}

var lightPalette = palette{
	user: shade{226, 234, 246, 254},
	tool: shade{228, 242, 231, 194},
	code: shade{242, 244, 248, 255},
	dock: shade{235, 238, 243, 254},
	err:  shade{250, 228, 228, 224},
}

var themePalette = loadPalette()

// loadPalette picks the light surfaces when COLORFGBG reports a bright
// background ("fg;bg" with bg >= 7); otherwise it assumes a dark terminal.
func loadPalette() palette {
	if fgbg := os.Getenv("COLORFGBG"); fgbg != "" {
		parts := strings.Split(fgbg, ";")
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil && n >= 7 {
			return lightPalette
		}
	}
	return darkPalette
}

// bg is the SGR sequence that fills a band, or "" when colour is off or the
// terminal cannot show an RGB/256 background.
func (b bandKind) bg() string {
	if !ansi {
		return ""
	}
	var s shade
	switch b {
	case bandUser:
		s = themePalette.user
	case bandTool:
		s = themePalette.tool
	case bandCode:
		s = themePalette.code
	case bandError:
		s = themePalette.err
	case bandDock:
		s = themePalette.dock
	default:
		return ""
	}
	if truecolor {
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", s.r, s.g, s.b)
	}
	if color256 {
		return fmt.Sprintf("\x1b[48;5;%dm", s.idx)
	}
	return ""
}

// bandLine paints one already-styled line as a full-width band: padX columns of
// inner margin, padded to width, with the background re-applied after every SGR
// reset so inner bold/colour spans do not punch holes in it.
func bandLine(s string, width, padX int, bg string) string {
	if bg == "" {
		return s
	}
	inner := width - padX*2
	if inner < 1 {
		inner = 1
		padX = 0
	}
	if vis := visibleWidth(s); vis > inner {
		s = clipStyled(s, inner)
	} else if vis < inner {
		s += strings.Repeat(" ", inner-vis)
	}
	line := strings.Repeat(" ", padX) + s + strings.Repeat(" ", padX)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bg)
	return bg + line + "\x1b[0m"
}

// visibleWidth is how many terminal cells a styled string occupies.
func visibleWidth(s string) int {
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				if s[j] >= 0x40 && s[j] <= 0x7e {
					j++
					break
				}
				j++
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w += runeCells(r)
		i += size
	}
	return w
}
