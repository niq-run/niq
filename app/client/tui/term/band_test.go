package term

import "testing"

// TestParseOSCRGB pins the reply forms a terminal uses: 1-4 hex digits per
// channel, and the #rrggbb spelling some report.
func TestParseOSCRGB(t *testing.T) {
	cases := []struct {
		in   string
		want shade
		ok   bool
	}{
		{"rgb:0000/0000/0000", shade{0, 0, 0, 0}, true},
		{"rgb:ffff/ffff/ffff", shade{255, 255, 255, 0}, true},
		{"rgb:1e1e/1e1e/1e1e", shade{30, 30, 30, 0}, true},
		{"rgb:ff/80/00", shade{255, 128, 0, 0}, true},
		{"rgb:0/0/0", shade{0, 0, 0, 0}, true},
		{"#282c34", shade{0x28, 0x2c, 0x34, 0}, true},
		{"rgb:zz/00/00", shade{}, false},
		{"rgb:0/0", shade{}, false},
		{"nonsense", shade{}, false},
	}
	for _, c := range cases {
		got, ok := ParseOSCRGB(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseOSCRGB(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestTerminalReportAdaptsPalette pins the adaptation: a light background or a
// light colour-scheme report switches to the light palette, a dark one switches
// back, and an unrelated report changes nothing.
func TestTerminalReportAdaptsPalette(t *testing.T) {
	oldPalette, oldLight := themePalette, lightTheme
	defer func() { themePalette, lightTheme = oldPalette, oldLight }()

	themePalette, lightTheme = darkPalette, false
	if changed := TerminalReport("11;rgb:ffff/ffff/ffff"); !changed || !lightTheme {
		t.Fatalf("white background: changed=%v light=%v", changed, lightTheme)
	}
	if themePalette.tool != lightPalette.tool {
		t.Fatalf("palette not switched to light: %+v", themePalette)
	}
	if changed := TerminalReport("11;rgb:ffff/ffff/ffff"); changed {
		t.Fatal("same theme reported as a change")
	}

	if changed := TerminalReport("11;rgb:1010/1010/1010"); !changed || lightTheme {
		t.Fatalf("dark background: changed=%v light=%v", changed, lightTheme)
	}
	if themePalette.tool != darkPalette.tool {
		t.Fatalf("palette not switched to dark: %+v", themePalette)
	}

	// A colour-scheme notice from mode 2031 carries the same decision.
	if changed := TerminalReport("997;2"); !changed || !lightTheme {
		t.Fatalf("997;2: changed=%v light=%v", changed, lightTheme)
	}
	if changed := TerminalReport("997;1"); !changed || lightTheme {
		t.Fatalf("997;1: changed=%v light=%v", changed, lightTheme)
	}
	for _, junk := range []string{"", "4;0", "11;rgb:zz/z/z", "997;9"} {
		if TerminalReport(junk) {
			t.Fatalf("junk report %q changed the palette", junk)
		}
	}
}
