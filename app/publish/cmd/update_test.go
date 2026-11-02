package main

import "testing"

// TestNewerRelease pins the comparison the hint and `niq update` both rely on:
// only the release number decides, so a dev build ("dev", or v0.14.1-18-gabc)
// never looks newer than a published 0.14.1, and the git suffix is ignored.
func TestNewerRelease(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.14.1", "0.14.0", true},
		{"0.14.1", "v0.14.0", true},
		{"0.15.0", "0.14.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.14.1", "0.14.1", false},
		{"0.14.1", "0.14.1-18-g2cf506d5", false}, // same release, newer build
		{"0.14.0", "0.14.1", false},
		{"0.14.1", "dev", false}, // nothing to compare
		{"garbage", "0.14.0", false},
	}
	for _, c := range cases {
		if got := newerRelease(c.latest, c.current); got != c.want {
			t.Errorf("newerRelease(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestSemverParts(t *testing.T) {
	if got, ok := semverParts("v0.14.1-18-g2cf506d5"); !ok || got != [3]int{0, 14, 1} {
		t.Fatalf("semverParts(describe) = %v %v, want [0 14 1] true", got, ok)
	}
	if got, ok := semverParts("1.2"); !ok || got != [3]int{1, 2, 0} {
		t.Fatalf("semverParts(short) = %v %v, want [1 2 0] true", got, ok)
	}
	for _, bad := range []string{"", "dev", "1.x.0"} {
		if _, ok := semverParts(bad); ok {
			t.Errorf("semverParts(%q) reported a version", bad)
		}
	}
}
