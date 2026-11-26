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
	bandWarn
)

// shade is one surface: an RGB triplet for truecolor and an ANSI-256 index for
// terminals that only do 256.
type shade struct {
	r, g, b int
	idx     int
}

type palette struct {
	user, tool, code, err, warn shade
}

var darkPalette = palette{
	user: shade{46, 58, 76, 237},
	// pi's own tokens: toolSuccessBg okhsl(158 46% 25%) and toolErrorBg
	// okhsl(19 54% 25%), converted to sRGB.
	tool: shade{37, 65, 49, 22},
	code: shade{26, 29, 35, 234},
	err:  shade{91, 40, 42, 52},
	warn: shade{62, 49, 26, 58},
}

var lightPalette = palette{
	user: shade{226, 234, 246, 254},
	tool: shade{222, 233, 225, 194}, // okhsl(156 21% 91%)
	code: shade{242, 244, 248, 255},
	err:  shade{238, 226, 225, 224}, // okhsl(24 23% 91%)
	warn: shade{239, 231, 219, 230},
}

var themePalette, lightTheme = loadPalette()

// Ink travels with our own surfaces: a panel's text must be legible on it whatever
// the terminal's default foreground is.
var (
	darkInk  = shade{226, 232, 240, 0}
	lightInk = shade{28, 34, 44, 0}
)

// blockSurface is a panel's surface: its background plus the ink that travels
// with it.
func blockSurface(band bandKind) string {
	bg := band.bg()
	if bg == "" || !ansi || !truecolor {
		return bg
	}
	ink := darkInk
	if lightTheme {
		ink = lightInk
	}
	var s shade
	switch band {
	case bandUser:
		s = themePalette.user
	case bandTool:
		s = themePalette.tool
	case bandCode:
		s = themePalette.code
	case bandError:
		s = themePalette.err
	case bandWarn:
		s = themePalette.warn
	default:
		return bg
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm", ink.r, ink.g, ink.b, s.r, s.g, s.b)
}

// terminalReport applies a colour report from the terminal: its background colour
// (OSC 11) or a colour-scheme notice (CSI ? 997 n). It reports whether the
// palette changed, so the caller can repaint.
//
// Asking the terminal is what makes a light theme work: COLORFGBG is only set by
// some terminals, so without this almost everything falls back to the dark
// palette even on a white background.
func terminalReport(text string) bool {
	var want bool
	switch {
	case strings.HasPrefix(text, "11;"):
		bg, ok := parseOSCRGB(strings.TrimPrefix(text, "11;"))
		if !ok {
			return false
		}
		// Perceived lightness, the usual 299/587/114 weights.
		want = 0.299*float64(bg.r)+0.587*float64(bg.g)+0.114*float64(bg.b) > 128
	case strings.HasPrefix(text, "997;"):
		switch strings.TrimPrefix(text, "997;") {
		case "1":
			want = false
		case "2":
			want = true
		default:
			return false
		}
	default:
		return false
	}
	if want == lightTheme {
		return false
	}
	lightTheme = want
	if want {
		themePalette = lightPalette
	} else {
		themePalette = darkPalette
	}
	return true
}

// parseOSCRGB decodes an OSC colour value: "rgb:rrrr/gggg/bbbb" (1-4 hex digits
// per channel) or "#rrggbb".
func parseOSCRGB(s string) (shade, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "#") {
		if len(s) != 7 {
			return shade{}, false
		}
		var v [3]int
		for i := 0; i < 3; i++ {
			n, err := strconv.ParseUint(s[1+i*2:3+i*2], 16, 8)
			if err != nil {
				return shade{}, false
			}
			v[i] = int(n)
		}
		return shade{v[0], v[1], v[2], 0}, true
	}
	s = strings.TrimPrefix(s, "rgb:")
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return shade{}, false
	}
	var v [3]int
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) == 0 || len(p) > 4 {
			return shade{}, false
		}
		n, err := strconv.ParseUint(p, 16, 16)
		if err != nil {
			return shade{}, false
		}
		max := uint64(1)<<(4*uint(len(p))) - 1
		v[i] = int(uint64(n) * 255 / max)
	}
	return shade{v[0], v[1], v[2], 0}, true
}

// loadPalette picks the light surfaces when COLORFGBG reports a bright
// background ("fg;bg" with bg >= 7); otherwise it assumes a dark terminal.
func loadPalette() (palette, bool) {
	if fgbg := os.Getenv("COLORFGBG"); fgbg != "" {
		parts := strings.Split(fgbg, ";")
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil && n >= 7 {
			return lightPalette, true
		}
	}
	return darkPalette, false
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
	case bandWarn:
		s = themePalette.warn
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

// toolCall and toolOut are the two foregrounds of a tool card, matching pi's:
// the call line is bright so it leads, and the reply recedes to a soft tone on
// the same green surface. Both are tuned for the surface they sit on, so they
// flip with the light/dark palette.
func toolCall(s string) string {
	if !ansi {
		return s
	}
	if truecolor {
		if lightTheme {
			return "\x1b[1;38;2;22;42;32m" + s + "\x1b[0m"
		}
		return "\x1b[1;38;2;236;246;240m" + s + "\x1b[0m"
	}
	if lightTheme {
		return paint("1;30", s)
	}
	return paint("1;97", s)
}

func toolOut(s string) string {
	if !ansi {
		return s
	}
	if truecolor {
		if lightTheme {
			return "\x1b[38;2;82;96;88m" + s + "\x1b[0m"
		}
		return "\x1b[38;2;186;200;192m" + s + "\x1b[0m"
	}
	if lightTheme {
		return paint("38;5;240", s)
	}
	return paint("38;5;151", s)
}

// toolReply is the dimmest card tone: the reply rows under a call, a step below
// toolOut so the call line leads and the reply recedes. On the light surface it
// goes lighter (toward the background); on the dark one, greyer.
func toolReply(s string) string {
	if !ansi {
		return s
	}
	if truecolor {
		if lightTheme {
			return "\x1b[38;2;112;126;118m" + s + "\x1b[0m"
		}
		return "\x1b[38;2;150;168;160m" + s + "\x1b[0m"
	}
	if lightTheme {
		return paint("38;5;243", s)
	}
	return paint("38;5;145", s)
}

// toolFail is the reply tone for a failed or rejected call: readable red rather
// than the terminal's default, which is too dark on the error surface.
func toolFail(s string) string {
	if !ansi {
		return s
	}
	if truecolor {
		if lightTheme {
			return "\x1b[38;2;120;30;30m" + s + "\x1b[0m"
		}
		return "\x1b[38;2;238;170;170m" + s + "\x1b[0m"
	}
	return paint("38;5;210", s)
}
