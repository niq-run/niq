// Package term is the terminal foundation the tui screens are drawn on: ANSI
// styling, the dark/light palette and background bands, cell-accurate width
// measurement, and the ANSI-aware string clip/slice that keeps a styled line from
// being cut in the middle of an escape.
//
// It depends only on x/term, so a renderer (the markdown viewer, say) can be
// reused without dragging the session, the bus or the event model along. Width is
// measured in cells, never bytes or runes: an East Asian wide rune or an emoji
// takes two cells and everything else one, so a CJK name lines up with an ASCII
// one and a wide rune is never split.
package term

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	xterm "golang.org/x/term"
)

// ── Colour capability ──

var Ansi = func() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return xterm.IsTerminal(int(os.Stdout.Fd()))
}()

var Truecolor = strings.Contains(os.Getenv("COLORTERM"), "truecolor") ||
	strings.Contains(os.Getenv("COLORTERM"), "24bit") ||
	strings.Contains(os.Getenv("TERM"), "truecolor")

var color256 = Truecolor || strings.Contains(os.Getenv("TERM"), "256color")

func paint(code, s string) string {
	if !Ansi {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// ── Foreground styles ──

func Bold(s string) string  { return paint("1", s) }
func Dim(s string) string   { return paint("2", s) }
func Green(s string) string { return paint("32", s) }
func Red(s string) string   { return paint("31", s) }

// Underline marks the human's own messages so they stand out in the transcript;
// Italic and Underline again are for links, and CodeText for inline/block code.
func Underline(s string) string { return paint("4", s) }
func Italic(s string) string    { return paint("3", s) }
func CodeText(s string) string  { return paint("36", s) }

// Reverse paints s as a filled block: the terminal swaps foreground and
// background (a selection highlight), leaving the surrounding style intact.
func Reverse(s string) string {
	if !Ansi {
		return s
	}
	return "\x1b[7m" + s + "\x1b[27m"
}

// Gray is a muted foreground for thinking: readable but clearly secondary, and
// not a colour that competes with the transcript.
func Gray(s string) string     { return paint("90", s) }
func GrayBold(s string) string { return paint("1;90", s) }

// WarnText is the body tone for a warning row on the amber surface: a soft warm
// grey, since plain dim would sink into the band.
func WarnText(s string) string {
	if !Ansi {
		return s
	}
	if Truecolor {
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
func CodeKeyword(s string) string { return paint("1;36", s) }
func CodeString(s string) string  { return paint("32", s) }
func CodeNumber(s string) string  { return paint("33", s) }
func CodeComment(s string) string { return paint("2", s) }

// Speaker and event-title colours. They give each kind of line its own hue so a
// transcript can be scanned without reading every label: the human is green, a
// worker's answer keeps the brand blue, thinking is magenta, ordinary events are
// cyan, warnings yellow and failures red.
func GreenBold(s string) string    { return paint("1;32", s) }
func MagentaBold(s string) string  { return paint("1;35", s) }
func CyanBold(s string) string     { return paint("1;36", s) }
func YellowBright(s string) string { return paint("1;93", s) }
func RedBold(s string) string      { return paint("1;31", s) }

func Brand(s string) string {
	if !Ansi {
		return s
	}
	if Truecolor {
		return "\x1b[38;2;60;120;180m" + s + "\x1b[0m"
	}
	return "\x1b[38;5;67m" + s + "\x1b[0m"
}

func BrandBold(s string) string {
	if !Ansi {
		return s
	}
	if Truecolor {
		return "\x1b[1;38;2;60;120;180m" + s + "\x1b[0m"
	}
	return "\x1b[1;38;5;67m" + s + "\x1b[0m"
}

// BrandBg is the brand accent as a background. It paints the frame the modals
// sit in, giving a popup the blue outline classic terminal programs use.
func BrandBg() string {
	if !Ansi {
		return ""
	}
	if Truecolor {
		return "\x1b[48;2;60;120;180m"
	}
	if color256 {
		return "\x1b[48;5;67m"
	}
	return "\x1b[44m"
}

// ── Cell measurement ──

// DisplayWidth is how many terminal cells s occupies: an East Asian wide rune
// takes two, everything else one. Project ids are user text (SanitizeID only
// swaps separators), so padding them by byte length would misalign a CJK name
// against an ASCII one.
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		if IsWide(r) {
			w += 2
			continue
		}
		w++
	}
	return w
}

// VisibleWidth is how many terminal cells a styled string occupies.
func VisibleWidth(s string) int {
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
		w += RuneCells(r)
		i += size
	}
	return w
}

// RuneCells is how many terminal cells one rune occupies.
func RuneCells(r rune) int {
	if IsWide(r) {
		return 2
	}
	return 1
}

// IsWide reports whether r is East Asian wide or an emoji, the cases a terminal
// renders in two cells.
func IsWide(r rune) bool {
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
	case isEmojiPresentation(r): // BMP emoji a terminal draws two cells wide (✅, ⭐, ⌛…)
		return true
	case r >= 0x1F000 && r <= 0x1FAFF: // emoji, pictographs, mahjong, flags
		return true
	case r >= 0x20000 && r <= 0x3FFFD:
		return true
	}
	return false
}

// isEmojiPresentation reports whether r is one of the BMP code points whose
// default emoji presentation a terminal renders two cells wide. These sit below
// the 0x1F000 block (misc symbols, dingbats, …) but still take two cells, so the
// cell math has to count them like CJK or the frame drifts. The table is the
// Emoji_Presentation=Yes set for the Basic Multilingual Plane.
func isEmojiPresentation(r rune) bool {
	switch {
	case r >= 0x231A && r <= 0x231B,
		r >= 0x23E9 && r <= 0x23EC,
		r == 0x23F0, r == 0x23F3,
		r >= 0x25FD && r <= 0x25FE,
		r >= 0x2614 && r <= 0x2615,
		r >= 0x2648 && r <= 0x2653,
		r == 0x267F, r == 0x2693, r == 0x26A1,
		r >= 0x26AA && r <= 0x26AB,
		r >= 0x26BD && r <= 0x26BE,
		r >= 0x26C4 && r <= 0x26C5,
		r == 0x26CE, r == 0x26D4, r == 0x26EA,
		r >= 0x26F2 && r <= 0x26F3,
		r == 0x26F5, r == 0x26FA, r == 0x26FD,
		r == 0x2705,
		r >= 0x270A && r <= 0x270B,
		r == 0x2728, r == 0x274C, r == 0x274E,
		r >= 0x2753 && r <= 0x2755,
		r == 0x2757,
		r >= 0x2795 && r <= 0x2797,
		r == 0x27B0, r == 0x27BF,
		r >= 0x2B1B && r <= 0x2B1C,
		r == 0x2B50, r == 0x2B55:
		return true
	}
	return false
}

// TruncateCells cuts s to at most max cells, never splitting a wide rune.
func TruncateCells(s string, max int) string {
	var b strings.Builder
	cells := 0
	for _, r := range s {
		w := RuneCells(r)
		if cells+w > max {
			break
		}
		cells += w
		b.WriteRune(r)
	}
	return b.String()
}

// ── ANSI-aware clip and slice ──

// Clip truncates s to at most width terminal cells, copying ANSI escape
// sequences through without counting them and closing any style it cut off.
func Clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	cells := 0
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
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w := RuneCells(r)
		if cells+w > width {
			break
		}
		cells += w
		b.WriteString(s[i : i+size])
		i += size
	}
	if i < len(s) {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// csiLen is the byte length of the ANSI escape starting at s[i], or 0 when s[i]
// does not start one.
func csiLen(s string, i int) int {
	if i+1 >= len(s) || s[i] != 0x1b || s[i+1] != '[' {
		return 0
	}
	for j := i + 2; j < len(s); j++ {
		if s[j] >= 0x40 && s[j] <= 0x7e {
			return j + 1 - i
		}
	}
	return len(s) - i
}

// Slice returns the cells [from, to) of a styled line. It replays the SGR state
// up to from so a colour or background continues across the cut (a banded tool
// card stays banded), and pads with spaces when the range runs past the line's
// visible width.
func Slice(s string, from, to int) string {
	if to <= from {
		return ""
	}
	width := to - from
	var state, out strings.Builder
	cells := 0
	i := 0
	// Replay everything before from into state: the last escape in wins is the
	// colour at the cut.
	for i < len(s) && cells < from {
		if n := csiLen(s, i); n > 0 {
			state.WriteString(s[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		cells += RuneCells(r)
		i += size
	}
	emitted := 0
	if cells > from { // a wide rune straddled the left edge
		out.WriteByte(' ')
		emitted = 1
	}
	for i < len(s) && emitted < width {
		if n := csiLen(s, i); n > 0 {
			out.WriteString(s[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w := RuneCells(r)
		if emitted+w > width {
			break
		}
		out.WriteString(s[i : i+size])
		emitted += w
		i += size
	}
	for emitted < width {
		out.WriteByte(' ')
		emitted++
	}
	res := state.String() + out.String()
	if strings.Contains(res, "\x1b[") {
		res += "\x1b[0m"
	}
	return res
}

// ── Background bands and palette ──

// Background bands and rules, in the spirit of pi's theme: the transcript reads
// as layers because the human's turns, tool rows and code blocks sit on their own
// subtle surfaces, while prose stays on the terminal's background.
//
// pi derives a full palette from the terminal's reported background (OSC 11) and
// falls back by lightness. We keep that shape but not the machinery: two fixed
// palettes (dark and light), chosen from COLORFGBG when it is present, and
// downgraded to 256 colours or no band at all (NO_COLOR / a 16-colour terminal).

// BandKind names the surface a block is painted on. BandNone leaves the
// terminal's own background.
type BandKind int

const (
	BandNone BandKind = iota
	BandUser
	BandTool
	BandCode
	BandError
	BandWarn
)

// shade is one surface: an RGB triplet for Truecolor and an ANSI-256 index for
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

// BlockSurface is a panel's surface: its background plus the ink that travels
// with it.
func BlockSurface(band BandKind) string {
	bg := band.Bg()
	if bg == "" || !Ansi || !Truecolor {
		return bg
	}
	ink := darkInk
	if lightTheme {
		ink = lightInk
	}
	var s shade
	switch band {
	case BandUser:
		s = themePalette.user
	case BandTool:
		s = themePalette.tool
	case BandCode:
		s = themePalette.code
	case BandError:
		s = themePalette.err
	case BandWarn:
		s = themePalette.warn
	default:
		return bg
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm", ink.r, ink.g, ink.b, s.r, s.g, s.b)
}

// TerminalReport applies a colour report from the terminal: its background colour
// (OSC 11) or a colour-scheme notice (CSI ? 997 n). It reports whether the
// palette changed, so the caller can repaint.
//
// Asking the terminal is what makes a light theme work: COLORFGBG is only set by
// some terminals, so without this almost everything falls back to the dark
// palette even on a white background.
func TerminalReport(text string) bool {
	var want bool
	switch {
	case strings.HasPrefix(text, "11;"):
		bg, ok := ParseOSCRGB(strings.TrimPrefix(text, "11;"))
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

// ParseOSCRGB decodes an OSC colour value: "rgb:rrrr/gggg/bbbb" (1-4 hex digits
// per channel) or "#rrggbb".
func ParseOSCRGB(s string) (shade, bool) {
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

// Bg is the SGR sequence that fills a band, or "" when colour is off or the
// terminal cannot show an RGB/256 background.
func (b BandKind) Bg() string {
	if !Ansi {
		return ""
	}
	var s shade
	switch b {
	case BandUser:
		s = themePalette.user
	case BandTool:
		s = themePalette.tool
	case BandCode:
		s = themePalette.code
	case BandError:
		s = themePalette.err
	case BandWarn:
		s = themePalette.warn
	default:
		return ""
	}
	if Truecolor {
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", s.r, s.g, s.b)
	}
	if color256 {
		return fmt.Sprintf("\x1b[48;5;%dm", s.idx)
	}
	return ""
}

// BandLine paints one already-styled line as a full-width band: padX columns of
// inner margin, padded to width, with the background re-applied after every SGR
// reset so inner bold/colour spans do not punch holes in it.
func BandLine(s string, width, padX int, bg string) string {
	if bg == "" {
		return s
	}
	inner := width - padX*2
	if inner < 1 {
		inner = 1
		padX = 0
	}
	if vis := VisibleWidth(s); vis > inner {
		s = Clip(s, inner)
	} else if vis < inner {
		s += strings.Repeat(" ", inner-vis)
	}
	line := strings.Repeat(" ", padX) + s + strings.Repeat(" ", padX)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bg)
	return bg + line + "\x1b[0m"
}

// ToolCall and ToolOut are the two foregrounds of a tool card, matching pi's:
// the call line is bright so it leads, and the reply recedes to a soft tone on
// the same green surface. Both are tuned for the surface they sit on, so they
// flip with the light/dark palette.
func ToolCall(s string) string {
	if !Ansi {
		return s
	}
	if Truecolor {
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

func ToolOut(s string) string {
	if !Ansi {
		return s
	}
	if Truecolor {
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

// ToolReply is the dimmest card tone: the reply rows under a call, a step below
// ToolOut so the call line leads and the reply recedes. On the light surface it
// goes lighter (toward the background); on the dark one, greyer.
func ToolReply(s string) string {
	if !Ansi {
		return s
	}
	if Truecolor {
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

// ToolFail is the reply tone for a failed or rejected call: readable red rather
// than the terminal's default, which is too dark on the error surface.
func ToolFail(s string) string {
	if !Ansi {
		return s
	}
	if Truecolor {
		if lightTheme {
			return "\x1b[38;2;120;30;30m" + s + "\x1b[0m"
		}
		return "\x1b[38;2;238;170;170m" + s + "\x1b[0m"
	}
	return paint("38;5;210", s)
}
