// Control-character handling shared by the composer and the event payloads.
package text

import "strings"

// IsControl reports whether r is a control character that must never be drawn or
// sent as message text. Newline is structural and tab is whitespace, so both stay.
func IsControl(r rune) bool {
	return r == 0x7f || (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x80 && r < 0xa0)
}

// StripEscapes removes whole ANSI escape sequences — CSI and OSC — from pasted
// text. Dropping only the ESC byte would leave the sequence's tail ("[31m") as
// visible junk in the message.
func StripEscapes(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i + 1
		if j >= len(s) {
			break
		}
		switch s[j] {
		case '[': // CSI: parameters, then a final byte in 0x40..0x7e
			j++
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
		case ']': // OSC: up to BEL or ST
			j++
			for j < len(s) && s[j] != 0x07 && s[j] != 0x1b {
				j++
			}
			if j < len(s) && s[j] == 0x07 {
				j++
			} else if j+1 < len(s) && s[j] == 0x1b && s[j+1] == '\\' {
				j += 2
			}
		default: // a two-byte escape such as ESC c
			j++
		}
		i = j
	}
	return b.String()
}
