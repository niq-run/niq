// Cell measurement: how many terminal cells a piece of text occupies — plain or
// carrying ANSI styling — and the small helpers wrapping and truncation are
// built on.
//
// Width is measured in cells, never in bytes or runes: an East Asian wide rune or
// an emoji takes two cells and everything else one, so a CJK name lines up with an
// ASCII one and a wide rune is never split across a boundary.
//
// This is the bottom layer. It depends on nothing else in the package, so it is
// safe to read first.
package tui

import (
	"strings"
	"unicode/utf8"
)

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

// runeCells is how many terminal cells one rune occupies.
func runeCells(r rune) int {
	if isWide(r) {
		return 2
	}
	return 1
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

// truncateCells cuts s to at most max cells, never splitting a wide rune.
func truncateCells(s string, max int) string {
	var b strings.Builder
	cells := 0
	for _, r := range s {
		w := runeCells(r)
		if cells+w > max {
			break
		}
		cells += w
		b.WriteRune(r)
	}
	return b.String()
}
