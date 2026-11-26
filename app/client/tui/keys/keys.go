// Package keys decodes terminal input: control bytes, CSI sequences, the Kitty and
// modifyOtherKeys spellings, bracketed paste, mouse reports and the terminal's
// colour/scheme replies — one keystroke at a time into a Press. It depends only on
// ../stdin, so any line-mode terminal app can reuse it; what a Press *means* is
// left to the caller (the chat view's keymap).
package keys

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

type PressKind int

const (
	PressOther PressKind = iota
	PressRune
	PressEnter
	PressNewline
	PressPaste
	PressMode
	PressBackspace
	PressDelete
	PressLeft
	PressRight
	PressUp
	PressDown
	PressHome
	PressEnd
	PressPageUp
	PressPageDown
	PressLineUp
	PressLineDown
	PressKillEnd
	PressKillWord
	PressYank
	PressAlt
	PressWheelUp
	PressWheelDown
	PressClick
	PressDrag
	PressRelease
	PressTerminal
	PressTarget
	PressView
	PressHelp
	PressEsc
	PressInterrupt
	PressTab
	PressBacktab
	PressPrevUser
	PressNextUser
)

// WheelStep is how many lines one wheel notch moves. One line keeps a trackpad
// or wheel feeling smooth and slow, as pi's does.
const WheelStep = 1

// MaxInputRows caps how many dock rows a multi-line message may occupy before it
// scrolls to keep the cursor visible.
const MaxInputRows = 8

type Press struct {
	Kind PressKind
	R    rune
	Text string // a bracketed paste's payload, or a terminal colour report
	X, Y int    // terminal cell (1-based) for mouse reports
}

// ReadKeys decodes stdin keystrokes onto out until the context ends.
func ReadKeys(ctx context.Context, out chan<- Press) {
	ReadKeysFrom(ctx, stdin.Stream(), stdin.SetDeadline, out)
}

// ReadKeysFrom is ReadKeys with the reader and deadline hook passed in, so the
// cancellation path can be tested without a terminal. Reads use a short deadline
// so the loop notices cancellation while idle; a keypress returns immediately.
func ReadKeysFrom(ctx context.Context, in *bufio.Reader, setDeadline func(time.Time) error, out chan<- Press) {
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
				case out <- Press{Kind: PressInterrupt}:
				case <-ctx.Done():
				}
				return
			}
			continue
		}
		_ = setDeadline(time.Time{})
		p := DecodePress(in, r)
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
func logPress(first rune, p Press) {
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
		time.Now().Format("15:04:05.000"), first, p.Kind, p.R, len(p.Text))
}

// DecodePress maps one rune to a Press, reading the rest of an escape sequence
// when the rune is Esc. Paging follows vim: ctrl+u/d and ctrl+b/f page, ctrl+y/e
// scroll one line, ctrl+p/n jump between user messages; ctrl+k clears the input; ctrl+t
// opens the worker popup; ctrl+? opens the help; ctrl+c leaves. Enter sends and
// ctrl+j (or shift+enter, where the terminal can spell it) starts a new line.
func DecodePress(in *bufio.Reader, r rune) Press {
	switch r {
	case '\r':
		return Press{Kind: PressEnter}
	case '\n':
		return Press{Kind: PressNewline}
	case '\t':
		return Press{Kind: PressTab}
	case 0x7f, 0x08:
		return Press{Kind: PressBackspace}
	case 0x1b:
		return decodeEscape(in)
	}
	if p, ok := ControlPress(r); ok {
		return p
	}
	if r >= 0x20 && r != 0x7f {
		return Press{Kind: PressRune, R: r}
	}
	return Press{Kind: PressOther}
}

// ControlPress maps a C0 byte to the key it stands for, reporting false for the
// bytes this view does not bind.
// ControlPress maps a C0 byte to the key it stands for, reporting false for the
// bytes this view does not bind. Inside the text field these are readline/Emacs
// bindings, which is what hands expect; Ctrl+U / Ctrl+D are the exception and page
// the transcript, as they do in less and vim. Everything else pages with
// PgUp/PgDn, ctrl+arrows, shift+arrows and the wheel.
func ControlPress(r rune) (Press, bool) {
	switch r {
	case 0x03: // Ctrl+C
		return Press{Kind: PressInterrupt}, true
	case 0x11: // Ctrl+Q: an alternate quit, for terminals or IMEs that eat ctrl+c
		return Press{Kind: PressInterrupt}, true
	case 0x01: // Ctrl+A: line start
		return Press{Kind: PressHome}, true
	case 0x05: // Ctrl+E: line end
		return Press{Kind: PressEnd}, true
	case 0x02: // Ctrl+B: char left
		return Press{Kind: PressLeft}, true
	case 0x06: // Ctrl+F: char right
		return Press{Kind: PressRight}, true
	case 0x10: // Ctrl+P: previous user message
		return Press{Kind: PressPrevUser}, true
	case 0x0e: // Ctrl+N: next user message
		return Press{Kind: PressNextUser}, true
	case 0x04: // Ctrl+D: page the transcript down
		return Press{Kind: PressPageDown}, true
	case 0x0b: // Ctrl+K: kill to end of line
		return Press{Kind: PressKillEnd}, true
	case 0x15: // Ctrl+U: page the transcript up
		return Press{Kind: PressPageUp}, true
	case 0x17: // Ctrl+W: kill previous word
		return Press{Kind: PressKillWord}, true
	case 0x19: // Ctrl+Y: yank
		return Press{Kind: PressYank}, true
	case 0x14: // Ctrl+T
		return Press{Kind: PressTarget}, true
	case 0x16: // Ctrl+V
		return Press{Kind: PressView}, true
	case 0x18: // Ctrl+X
		return Press{Kind: PressMode}, true
	case 0x1f: // Ctrl+? (also ctrl+/ and ctrl+_): the help
		return Press{Kind: PressHelp}, true
	}
	return Press{}, false
}

// decodeEscape classifies an escape sequence. A lone Esc cancels the worker menu;
// arrows, Home/End/Delete and PageUp/PageDown map to their keys. Shift+Enter
// reaches us as an escape once the terminal speaks the Kitty keyboard protocol or
// xterm's modifyOtherKeys, and alt+enter is the portable spelling of a newline.
func decodeEscape(in *bufio.Reader) Press {
	_ = stdin.SetDeadline(time.Now().Add(escRead))
	defer stdin.SetDeadline(time.Time{})
	next, _, err := in.ReadRune()
	if err != nil {
		return Press{Kind: PressEsc}
	}
	switch next {
	case '\r', '\n':
		return Press{Kind: PressNewline}
	case ']':
		// An OSC string: the reply to our background-colour query, or anything else
		// the terminal chose to send. It must be consumed here, never typed into the
		// composer.
		return decodeOSC(in)
	case 'O':
		final, _, err := in.ReadRune()
		if err != nil {
			return Press{Kind: PressOther}
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
		return Press{Kind: PressEsc}
	}
	// Alt+key arrives as ESC then the key. Word motion uses alt+b / alt+f, as in
	// readline; everything else alt-typed is ignored rather than read as a quit.
	return Press{Kind: PressAlt, R: next}
}

// decodeCSI parses a control sequence: an optional private prefix, numeric
// parameters and a final byte. Mouse reports take their own path; the rest are
// cursor keys, tilde keys, Kitty's `u` form and xterm's modifyOtherKeys form.
func decodeCSI(in *bufio.Reader) Press {
	r, _, err := in.ReadRune()
	if err != nil {
		return Press{Kind: PressOther}
	}
	if r == '<' {
		return DecodeMouse(in)
	}
	private := rune(0)
	if r == '?' || r == '>' || r == '=' {
		private = r
		if r, _, err = in.ReadRune(); err != nil {
			return Press{Kind: PressOther}
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
			return Press{Kind: PressOther}
		default:
			goto final
		}
		if r, _, err = in.ReadRune(); err != nil {
			return Press{Kind: PressOther}
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
			return Press{Kind: PressTerminal, Text: fmt.Sprintf("997;%d", params[1])}
		}
		return Press{Kind: PressOther}
	}
	switch r {
	case 'A':
		return arrowPress(params, PressUp, PressLineUp, PressPageUp)
	case 'B':
		return arrowPress(params, PressDown, PressLineDown, PressPageDown)
	case 'C':
		return Press{Kind: PressRight}
	case 'D':
		return Press{Kind: PressLeft}
	case 'H':
		return Press{Kind: PressHome}
	case 'F':
		return Press{Kind: PressEnd}
	case 'Z':
		// CSI Z is shift+tab.
		return Press{Kind: PressBacktab}
	case '~':
		return tildePress(in, params)
	case 'u':
		return csiU(params)
	}
	return Press{Kind: PressOther}
}

// arrowPress maps a vertical arrow with modifiers: plain moves the composer's
// line, shift scrolls the transcript by a line, ctrl pages it.
func arrowPress(params []int, plain, shifted, ctrled PressKind) Press {
	mod := 1
	if len(params) > 1 && params[1] > 0 {
		mod = params[1]
	}
	bits := mod - 1
	switch {
	case bits&4 != 0: // ctrl
		return Press{Kind: ctrled}
	case bits&1 != 0: // shift
		return Press{Kind: shifted}
	}
	return Press{Kind: plain}
}

// csiU decodes a Kitty keyboard-protocol key: `CSI code ; modifiers u`.
func csiU(params []int) Press {
	if len(params) == 0 {
		return Press{Kind: PressOther}
	}
	mod := 1
	if len(params) > 1 && params[1] > 0 {
		mod = params[1]
	}
	return KeyPress(params[0], mod)
}

// tildePress decodes the classic `CSI n ~` family, including xterm's
// modifyOtherKeys form `CSI 27 ; modifiers ; code ~`.
func tildePress(in *bufio.Reader, params []int) Press {
	if len(params) == 0 {
		return Press{Kind: PressOther}
	}
	switch params[0] {
	case 1, 7:
		return Press{Kind: PressHome}
	case 4, 8:
		return Press{Kind: PressEnd}
	case 3:
		return Press{Kind: PressDelete}
	case 5:
		return Press{Kind: PressPageUp}
	case 6:
		return Press{Kind: PressPageDown}
	case 27:
		if len(params) >= 3 {
			return KeyPress(params[2], params[1])
		}
	case 200:
		// Bracketed paste: the payload follows verbatim up to the end marker and is
		// not a key sequence, so it is read raw.
		_ = stdin.SetDeadline(time.Time{})
		return decodePaste(in)
	}
	return Press{Kind: PressOther}
}

// oscRead bounds how long an OSC string may take; a reply to our query arrives in
// one burst.
const oscRead = 250 * time.Millisecond

// decodeOSC consumes an OSC string (ESC ] … BEL or ST) and returns its payload.
// The only one we ask for is the background colour, but consuming any of them is
// what keeps a terminal's answer out of the message being typed.
func decodeOSC(in *bufio.Reader) Press {
	_ = stdin.SetDeadline(time.Now().Add(oscRead))
	defer func() { _ = stdin.SetDeadline(time.Time{}) }()
	var b strings.Builder
	for {
		r, _, err := in.ReadRune()
		if err != nil {
			return Press{Kind: PressTerminal, Text: b.String()}
		}
		if r == 0x07 { // BEL
			return Press{Kind: PressTerminal, Text: b.String()}
		}
		if r == 0x1b { // ST: ESC \
			_, _, _ = in.ReadRune()
			return Press{Kind: PressTerminal, Text: b.String()}
		}
		if r < 0x20 {
			// Any other control byte means this was not an OSC string after all; hand
			// it back so it is handled as the key it is (ctrl+c included).
			_ = in.UnreadRune()
			return Press{Kind: PressOther}
		}
		if b.Len() > 256 {
			return Press{Kind: PressTerminal, Text: b.String()}
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
func decodePaste(in *bufio.Reader) Press {
	var b strings.Builder
	window := make([]rune, 0, len(pasteEnd))
	for {
		_ = stdin.SetDeadline(time.Now().Add(pasteRead))
		r, _, err := in.ReadRune()
		if err != nil {
			return Press{Kind: PressPaste, Text: b.String()}
		}
		b.WriteRune(r)
		window = append(window, r)
		if len(window) > len(pasteEnd) {
			window = window[1:]
		}
		if len(window) == len(pasteEnd) && string(window) == pasteEnd {
			s := b.String()
			return Press{Kind: PressPaste, Text: s[:len(s)-len(pasteEnd)]}
		}
		if b.Len() > maxPasteBytes {
			return Press{Kind: PressPaste, Text: b.String()}
		}
	}
}

// ss3Press handles the ESC O forms the application cursor-key mode uses.
func ss3Press(final rune) Press {
	switch final {
	case 'A':
		return Press{Kind: PressUp}
	case 'B':
		return Press{Kind: PressDown}
	case 'C':
		return Press{Kind: PressRight}
	case 'D':
		return Press{Kind: PressLeft}
	case 'H':
		return Press{Kind: PressHome}
	case 'F':
		return Press{Kind: PressEnd}
	case 'M':
		return Press{Kind: PressEnter}
	}
	return Press{Kind: PressOther}
}

// KeyPress maps a codepoint and an xterm/Kitty modifier value (1 = none, 2 =
// shift, 3 = alt, 5 = ctrl, 6 = ctrl+shift, …) to a Press.
func KeyPress(code, mod int) Press {
	bits := mod - 1
	shift := bits&1 != 0
	alt := bits&2 != 0
	ctrl := bits&4 != 0
	switch code {
	case 13, 10:
		if shift {
			return Press{Kind: PressNewline}
		}
		return Press{Kind: PressEnter}
	case 27:
		return Press{Kind: PressEsc}
	case 127, 8:
		return Press{Kind: PressBackspace}
	case 9: // Tab
		if shift {
			return Press{Kind: PressBacktab}
		}
		return Press{Kind: PressTab}
	}
	if ctrl {
		if b, ok := controlByte(code); ok {
			return controlOr(b)
		}
	}
	if !ctrl && !alt && code >= 32 {
		return Press{Kind: PressRune, R: rune(code)}
	}
	return Press{Kind: PressOther}
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
// not in ControlPress (enter, newline, esc, backspace).
func controlOr(b rune) Press {
	if p, ok := ControlPress(b); ok {
		return p
	}
	switch b {
	case 13:
		return Press{Kind: PressEnter}
	case 10:
		return Press{Kind: PressNewline}
	case 0x1b:
		return Press{Kind: PressEsc}
	case 0x08, 0x7f:
		return Press{Kind: PressBackspace}
	}
	return Press{Kind: PressOther}
}

// DecodeMouse parses the rest of an SGR mouse report (“CSI < b ; x ; y M|m“).
// The wheel scrolls; a left button Press, drag and release drive the transcript's
// text selection so the mouse can select and copy inside the alternate screen.
func DecodeMouse(in *bufio.Reader) Press {
	var vals [3]int
	field := 0
	b, err := in.ReadByte()
	if err != nil {
		return Press{Kind: PressOther}
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
			return Press{Kind: PressOther}
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
			return Press{Kind: PressWheelUp}
		}
		return Press{Kind: PressWheelDown}
	case wheel, !left:
		return Press{Kind: PressOther}
	case motion:
		return Press{Kind: PressDrag, X: x, Y: y}
	case release:
		return Press{Kind: PressRelease, X: x, Y: y}
	default:
		return Press{Kind: PressClick, X: x, Y: y}
	}
}
