package tui

import (
	"strings"
	"testing"
)

// TestPickerTitleHeader pins that `niq i` leads with the control-plane header it
// is handed and only falls back to the version line when given none.
func TestPickerTitleHeader(t *testing.T) {
	title := pickerTitle("LINE-ONE\nLINE-TWO\n")
	for _, want := range []string{"LINE-ONE", "LINE-TWO", "enter select"} {
		if !strings.Contains(title, want) {
			t.Fatalf("picker title missing %q:\n%s", want, title)
		}
	}
	if strings.Contains(strings.ToLower(title), "version:") {
		t.Fatalf("a provided header should not also get the version line:\n%s", title)
	}

	// No header → the version line stands in, so the identity is never lost.
	if fb := pickerTitle(""); !strings.Contains(strings.ToLower(fb), "version") {
		t.Fatalf("empty header did not fall back to the version line:\n%s", fb)
	}
}
