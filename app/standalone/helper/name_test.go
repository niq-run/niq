package helper

import "testing"

func TestSafeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ok-id_1.x", "ok-id_1.x"},
		{"my proj/1", "my_proj_1"},
		{"a/b", "a_b"},
		{"a:b", "a_b"}, // a colon is a path separator on Windows
		{"/Users/pin/x", "Users_pin_x"},
		{"UPPER lower", "UPPER_lower"},
		{"tab\there", "tab_here"},
		{"", ""},
		{"../../etc/passwd", ".._.._etc_passwd"}, // no traversal survives
	}
	for _, c := range cases {
		if got := SafeName(c.in); got != c.want {
			t.Errorf("SafeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSafeNameIsIdempotent pins the property the callers rely on: applying the
// rule twice changes nothing, so a sanitized id can be sanitized again.
func TestSafeNameIsIdempotent(t *testing.T) {
	for _, in := range []string{"my proj/1", "a:b", "/Users/pin/x", "ok-id_1.x"} {
		once := SafeName(in)
		if twice := SafeName(once); twice != once {
			t.Errorf("SafeName(%q) = %q, then %q", in, once, twice)
		}
	}
}
