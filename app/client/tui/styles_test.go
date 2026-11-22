package tui

import "testing"

// TestDisplayWidth pins the column arithmetic the project table depends on:
// wide (CJK, kana, Hangul, emoji) runes occupy two cells, everything else one.
func TestDisplayWidth(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"demo", 4},
		{"值班", 4},
		{"a值班", 5},
		{"パネル", 6},
		{"🚀", 2},
		{"a-b_c", 5},
	} {
		if got := displayWidth(tc.in); got != tc.want {
			t.Errorf("displayWidth(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
