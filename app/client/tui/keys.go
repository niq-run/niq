// Keys: decoding terminal input (control bytes, CSI sequences, the Kitty and
// modifyOtherKeys spellings, bracketed paste, mouse, terminal reports) and
// applying one keypress to the view.
package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/niq-run/niq/app/client/tui/stdin"
)

const (
	// idleRead bounds how long the key reader lingers after the view closes, so
	// it never steals a keystroke from whatever runs next.
	idleRead = 200 * time.Millisecond
	// escRead tells a lone Esc from the start of an arrow sequence.
	escRead = 40 * time.Millisecond
)

// ── Keys ──

type pressKind int

const (
	pressOther pressKind = iota
	pressRune
	pressEnter
	pressNewline
	pressPaste
	pressMode
	pressBackspace
	pressDelete
	pressLeft
	pressRight
	pressUp
	pressDown
	pressHome
	pressEnd
	pressPageUp
	pressPageDown
	pressLineUp
	pressLineDown
	pressKillEnd
	pressKillWord
	pressYank
	pressAlt
	pressWheelUp
	pressWheelDown
	pressClick
	pressDrag
	pressRelease
	pressTerminal
	pressTarget
	pressView
	pressHelp
	pressEsc
	pressInterrupt
	pressTab
	pressBacktab
)

// wheelStep is how many lines one wheel notch moves. One line keeps a trackpad
// or wheel feeling smooth and slow, as pi's does.
const wheelStep = 1

// maxInputRows caps how many dock rows a multi-line message may occupy before it
// scrolls to keep the cursor visible.
const maxInputRows = 8

type press struct {
	kind pressKind
	r    rune
	text string // a bracketed paste's payload, or a terminal colour report
	x, y int    // terminal cell (1-based) for mouse reports
}

// readKeys decodes stdin keystrokes onto out until the context ends.
func readKeys(ctx context.Context, out chan<- press) {
	readKeysFrom(ctx, stdin.Stream(), stdin.SetDeadline, out)
}

// readKeysFrom is readKeys with the reader and deadline hook passed in, so the
// cancellation path can be tested without a terminal. Reads use a short deadline
// so the loop notices cancellation while idle; a keypress returns immediately.
func readKeysFrom(ctx context.Context, in *bufio.Reader, setDeadline func(time.Time) error, out chan<- press) {
	for {
		if ctx.Err() != nil {
			return
		}
		_ = setDeadline(time.Now().Add(idleRead))
		r, _, err := in.ReadRune()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			// A read error that is not an idle timeout nor end-of-input would
			// otherwise kill the keyboard for the rest of the session; end the input
			// loop only on EOF or a dead stdin.
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				select {
				case out <- press{kind: pressInterrupt}:
				case <-ctx.Done():
				}
				return
			}
			continue
		}
		_ = setDeadline(time.Time{})
		p := decodePress(in, r)
		logPress(r, p)
		select {
		case out <- p:
		case <-ctx.Done():
			return
		}
	}
}

// logPress appends every decoded keypress to the file named by NIQ_TUI_KEYS. It is
// off unless that variable is set: the point is to answer "is this key even
// arriving?" from inside a real terminal, where a debugger is not an option.
func logPress(first rune, p press) {
	path := os.Getenv("NIQ_TUI_KEYS")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s byte=%#x kind=%d rune=%q text=%d\n",
		time.Now().Format("15:04:05.000"), first, p.kind, p.r, len(p.text))
}

// decodePress maps one rune to a press, reading the rest of an escape sequence
// when the rune is Esc. Paging follows vim: ctrl+u/d and ctrl+b/f page, ctrl+y/e
// scroll one line, ctrl+p/n also step a line; ctrl+k clears the input; ctrl+t
// opens the worker popup; ctrl+? opens the help; ctrl+c leaves. Enter sends and
// ctrl+j (or shift+enter, where the terminal can spell it) starts a new line.
func decodePress(in *bufio.Reader, r rune) press {
	switch r {
	case '\r':
		return press{kind: pressEnter}
	case '\n':
		return press{kind: pressNewline}
	case '\t':
		return press{kind: pressTab}
	case 0x7f, 0x08:
		return press{kind: pressBackspace}
	case 0x1b:
		return decodeEscape(in)
	}
	if p, ok := controlPress(r); ok {
		return p
	}
	if r >= 0x20 && r != 0x7f {
		return press{kind: pressRune, r: r}
	}
	return press{kind: pressOther}
}

// controlPress maps a C0 byte to the key it stands for, reporting false for the
// bytes this view does not bind.
// controlPress maps a C0 byte to the key it stands for, reporting false for the
// bytes this view does not bind. Inside the text field these are readline/Emacs
// bindings, which is what hands expect; Ctrl+U / Ctrl+D are the exception and page
// the transcript, as they do in less and vim. Everything else pages with
// PgUp/PgDn, ctrl+arrows, shift+arrows and the wheel.
func controlPress(r rune) (press, bool) {
	switch r {
	case 0x03: // Ctrl+C
		return press{kind: pressInterrupt}, true
	case 0x11: // Ctrl+Q: an alternate quit, for terminals or IMEs that eat ctrl+c
		return press{kind: pressInterrupt}, true
	case 0x01: // Ctrl+A: line start
		return press{kind: pressHome}, true
	case 0x05: // Ctrl+E: line end
		return press{kind: pressEnd}, true
	case 0x02: // Ctrl+B: char left
		return press{kind: pressLeft}, true
	case 0x06: // Ctrl+F: char right
		return press{kind: pressRight}, true
	case 0x10: // Ctrl+P: line up
		return press{kind: pressUp}, true
	case 0x0e: // Ctrl+N: line down
		return press{kind: pressDown}, true
	case 0x04: // Ctrl+D: page the transcript down
		return press{kind: pressPageDown}, true
	case 0x0b: // Ctrl+K: kill to end of line
		return press{kind: pressKillEnd}, true
	case 0x15: // Ctrl+U: page the transcript up
		return press{kind: pressPageUp}, true
	case 0x17: // Ctrl+W: kill previous word
		return press{kind: pressKillWord}, true
	case 0x19: // Ctrl+Y: yank
		return press{kind: pressYank}, true
	case 0x14: // Ctrl+T
		return press{kind: pressTarget}, true
	case 0x16: // Ctrl+V
		return press{kind: pressView}, true
	case 0x18: // Ctrl+X
		return press{kind: pressMode}, true
	case 0x1f: // Ctrl+? (also ctrl+/ and ctrl+_): the help
		return press{kind: pressHelp}, true
	}
	return press{}, false
}

// decodeEscape classifies an escape sequence. A lone Esc cancels the worker menu;
// arrows, Home/End/Delete and PageUp/PageDown map to their keys. Shift+Enter
// reaches us as an escape once the terminal speaks the Kitty keyboard protocol or
// xterm's modifyOtherKeys, and alt+enter is the portable spelling of a newline.
func decodeEscape(in *bufio.Reader) press {
	_ = stdin.SetDeadline(time.Now().Add(escRead))
	defer stdin.SetDeadline(time.Time{})
	next, _, err := in.ReadRune()
	if err != nil {
		return press{kind: pressEsc}
	}
	switch next {
	case '\r', '\n':
		return press{kind: pressNewline}
	case ']':
		// An OSC string: the reply to our background-colour query, or anything else
		// the terminal chose to send. It must be consumed here, never typed into the
		// composer.
		return decodeOSC(in)
	case 'O':
		final, _, err := in.ReadRune()
		if err != nil {
			return press{kind: pressOther}
		}
		return ss3Press(final)
	case '[':
		return decodeCSI(in)
	}
	if next < 0x20 || next == 0x7f {
		// A control byte cannot be part of an escape sequence, so it is a key the
		// user pressed while we waited for the rest of one (a stale ESC from an IME
		// or a terminal reply, then ctrl+c, say). Put it back so the next read
		// handles it as itself — otherwise ctrl+c would be swallowed as "Esc".
		_ = in.UnreadRune()
		return press{kind: pressEsc}
	}
	// Alt+key arrives as ESC then the key. Word motion uses alt+b / alt+f, as in
	// readline; everything else alt-typed is ignored rather than read as a quit.
	return press{kind: pressAlt, r: next}
}

// decodeCSI parses a control sequence: an optional private prefix, numeric
// parameters and a final byte. Mouse reports take their own path; the rest are
// cursor keys, tilde keys, Kitty's `u` form and xterm's modifyOtherKeys form.
func decodeCSI(in *bufio.Reader) press {
	r, _, err := in.ReadRune()
	if err != nil {
		return press{kind: pressOther}
	}
	if r == '<' {
		return decodeMouse(in)
	}
	private := rune(0)
	if r == '?' || r == '>' || r == '=' {
		private = r
		if r, _, err = in.ReadRune(); err != nil {
			return press{kind: pressOther}
		}
	}
	var params []int
	cur, have := 0, false
	for {
		switch {
		case r >= '0' && r <= '9':
			cur = cur*10 + int(r-'0')
			have = true
		case r == ';' || r == ':':
			params = append(params, cur)
			cur, have = 0, false
		case r < 0x20:
			// A control byte inside a sequence means it was truncated (or the user
			// typed during the wait). Hand the byte back as its own key.
			_ = in.UnreadRune()
			return press{kind: pressOther}
		default:
			goto final
		}
		if r, _, err = in.ReadRune(); err != nil {
			return press{kind: pressOther}
		}
	}
final:
	if have || len(params) > 0 {
		params = append(params, cur)
	}
	if private != 0 {
		// A colour-scheme report is the one private sequence we act on; the rest
		// (Kitty flags, device attributes) are consumed and ignored.
		if r == 'n' && len(params) >= 2 && params[0] == 997 {
			return press{kind: pressTerminal, text: fmt.Sprintf("997;%d", params[1])}
		}
		return press{kind: pressOther}
	}
	switch r {
	case 'A':
		return arrowPress(params, pressUp, pressLineUp, pressPageUp)
	case 'B':
		return arrowPress(params, pressDown, pressLineDown, pressPageDown)
	case 'C':
		return press{kind: pressRight}
	case 'D':
		return press{kind: pressLeft}
	case 'H':
		return press{kind: pressHome}
	case 'F':
		return press{kind: pressEnd}
	case 'Z':
		// CSI Z is shift+tab.
		return press{kind: pressBacktab}
	case '~':
		return tildePress(in, params)
	case 'u':
		return csiU(params)
	}
	return press{kind: pressOther}
}

// arrowPress maps a vertical arrow with modifiers: plain moves the composer's
// line, shift scrolls the transcript by a line, ctrl pages it.
func arrowPress(params []int, plain, shifted, ctrled pressKind) press {
	mod := 1
	if len(params) > 1 && params[1] > 0 {
		mod = params[1]
	}
	bits := mod - 1
	switch {
	case bits&4 != 0: // ctrl
		return press{kind: ctrled}
	case bits&1 != 0: // shift
		return press{kind: shifted}
	}
	return press{kind: plain}
}

// csiU decodes a Kitty keyboard-protocol key: `CSI code ; modifiers u`.
func csiU(params []int) press {
	if len(params) == 0 {
		return press{kind: pressOther}
	}
	mod := 1
	if len(params) > 1 && params[1] > 0 {
		mod = params[1]
	}
	return keyPress(params[0], mod)
}

// tildePress decodes the classic `CSI n ~` family, including xterm's
// modifyOtherKeys form `CSI 27 ; modifiers ; code ~`.
func tildePress(in *bufio.Reader, params []int) press {
	if len(params) == 0 {
		return press{kind: pressOther}
	}
	switch params[0] {
	case 1, 7:
		return press{kind: pressHome}
	case 4, 8:
		return press{kind: pressEnd}
	case 3:
		return press{kind: pressDelete}
	case 5:
		return press{kind: pressPageUp}
	case 6:
		return press{kind: pressPageDown}
	case 27:
		if len(params) >= 3 {
			return keyPress(params[2], params[1])
		}
	case 200:
		// Bracketed paste: the payload follows verbatim up to the end marker and is
		// not a key sequence, so it is read raw.
		_ = stdin.SetDeadline(time.Time{})
		return decodePaste(in)
	}
	return press{kind: pressOther}
}

// oscRead bounds how long an OSC string may take; a reply to our query arrives in
// one burst.
const oscRead = 250 * time.Millisecond

// decodeOSC consumes an OSC string (ESC ] … BEL or ST) and returns its payload.
// The only one we ask for is the background colour, but consuming any of them is
// what keeps a terminal's answer out of the message being typed.
func decodeOSC(in *bufio.Reader) press {
	_ = stdin.SetDeadline(time.Now().Add(oscRead))
	defer func() { _ = stdin.SetDeadline(time.Time{}) }()
	var b strings.Builder
	for {
		r, _, err := in.ReadRune()
		if err != nil {
			return press{kind: pressTerminal, text: b.String()}
		}
		if r == 0x07 { // BEL
			return press{kind: pressTerminal, text: b.String()}
		}
		if r == 0x1b { // ST: ESC \
			_, _, _ = in.ReadRune()
			return press{kind: pressTerminal, text: b.String()}
		}
		if r < 0x20 {
			// Any other control byte means this was not an OSC string after all; hand
			// it back so it is handled as the key it is (ctrl+c included).
			_ = in.UnreadRune()
			return press{kind: pressOther}
		}
		if b.Len() > 256 {
			return press{kind: pressTerminal, text: b.String()}
		}
		b.WriteRune(r)
	}
}

// pasteEnd closes a bracketed paste.
const pasteEnd = "\x1b[201~"

const (
	// pasteRead bounds one read inside a paste: bytes that keep arriving are fine,
	// a paste that stalls is abandoned rather than blocking the reader forever.
	pasteRead = 2 * time.Second
	// maxPasteBytes caps what one bracketed paste may carry.
	maxPasteBytes = 8 << 20
)

// decodePaste reads a bracketed paste's body verbatim, up to the end marker.
// decodePaste reads a bracketed paste's body verbatim, up to the end marker. Each
// read has a deadline rather than none, so a paste whose end marker never arrives
// cannot wedge the reader — and a wedged reader would also stop ctrl+c.
func decodePaste(in *bufio.Reader) press {
	var b strings.Builder
	window := make([]rune, 0, len(pasteEnd))
	for {
		_ = stdin.SetDeadline(time.Now().Add(pasteRead))
		r, _, err := in.ReadRune()
		if err != nil {
			return press{kind: pressPaste, text: b.String()}
		}
		b.WriteRune(r)
		window = append(window, r)
		if len(window) > len(pasteEnd) {
			window = window[1:]
		}
		if len(window) == len(pasteEnd) && string(window) == pasteEnd {
			s := b.String()
			return press{kind: pressPaste, text: s[:len(s)-len(pasteEnd)]}
		}
		if b.Len() > maxPasteBytes {
			return press{kind: pressPaste, text: b.String()}
		}
	}
}

// ss3Press handles the ESC O forms the application cursor-key mode uses.
func ss3Press(final rune) press {
	switch final {
	case 'A':
		return press{kind: pressUp}
	case 'B':
		return press{kind: pressDown}
	case 'C':
		return press{kind: pressRight}
	case 'D':
		return press{kind: pressLeft}
	case 'H':
		return press{kind: pressHome}
	case 'F':
		return press{kind: pressEnd}
	case 'M':
		return press{kind: pressEnter}
	}
	return press{kind: pressOther}
}

// keyPress maps a codepoint and an xterm/Kitty modifier value (1 = none, 2 =
// shift, 3 = alt, 5 = ctrl, 6 = ctrl+shift, …) to a press.
func keyPress(code, mod int) press {
	bits := mod - 1
	shift := bits&1 != 0
	alt := bits&2 != 0
	ctrl := bits&4 != 0
	switch code {
	case 13, 10:
		if shift {
			return press{kind: pressNewline}
		}
		return press{kind: pressEnter}
	case 27:
		return press{kind: pressEsc}
	case 127, 8:
		return press{kind: pressBackspace}
	case 9: // Tab
		if shift {
			return press{kind: pressBacktab}
		}
		return press{kind: pressTab}
	}
	if ctrl {
		if b, ok := controlByte(code); ok {
			return controlOr(b)
		}
	}
	if !ctrl && !alt && code >= 32 {
		return press{kind: pressRune, r: rune(code)}
	}
	return press{kind: pressOther}
}

// controlByte is the C0 byte a ctrl+<key> combination produces.
func controlByte(code int) (rune, bool) {
	switch {
	case code >= 'a' && code <= 'z':
		return rune(code-'a') + 1, true
	case code >= 'A' && code <= 'Z':
		return rune(code-'A') + 1, true
	case code == ' ', code == '@':
		return 0x00, true
	case code == '[':
		return 0x1b, true
	case code == '\\':
		return 0x1c, true
	case code == ']':
		return 0x1d, true
	case code == '^':
		return 0x1e, true
	case code == '_', code == '?', code == '/':
		return 0x1f, true
	}
	return 0, false
}

// controlOr resolves a control byte to its binding, covering the few that are
// not in controlPress (enter, newline, esc, backspace).
func controlOr(b rune) press {
	if p, ok := controlPress(b); ok {
		return p
	}
	switch b {
	case 13:
		return press{kind: pressEnter}
	case 10:
		return press{kind: pressNewline}
	case 0x1b:
		return press{kind: pressEsc}
	case 0x08, 0x7f:
		return press{kind: pressBackspace}
	}
	return press{kind: pressOther}
}

// decodeMouse parses the rest of an SGR mouse report (“CSI < b ; x ; y M|m“).
// The wheel scrolls; a left button press, drag and release drive the transcript's
// text selection so the mouse can select and copy inside the alternate screen.
func decodeMouse(in *bufio.Reader) press {
	var vals [3]int
	field := 0
	b, err := in.ReadByte()
	if err != nil {
		return press{kind: pressOther}
	}
	for {
		switch {
		case b >= '0' && b <= '9':
			vals[field] = vals[field]*10 + int(b-'0')
		case b == ';' && field < 2:
			field++
		default:
			goto done
		}
		b, err = in.ReadByte()
		if err != nil {
			return press{kind: pressOther}
		}
	}
done:
	button, x, y := vals[0], vals[1], vals[2]
	release := b == 'm'
	wheel := button&64 != 0
	motion := button&32 != 0
	left := button&3 == 0
	switch {
	case wheel && !release:
		if button&1 == 0 {
			return press{kind: pressWheelUp}
		}
		return press{kind: pressWheelDown}
	case wheel, !left:
		return press{kind: pressOther}
	case motion:
		return press{kind: pressDrag, x: x, y: y}
	case release:
		return press{kind: pressRelease, x: x, y: y}
	default:
		return press{kind: pressClick, x: x, y: y}
	}
}
