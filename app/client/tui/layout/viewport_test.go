package layout

import "testing"

// TestViewportFollow pins the default: a viewport that follows keeps its window
// on the last screenful as content grows, and grows no scrollback above it.
func TestViewportFollow(t *testing.T) {
	v := Viewport{Follow: true}
	v.Resize(10)
	v.SetTotal(5)
	if start, _ := v.Window(); start != 0 {
		t.Fatalf("short content start = %d, want 0", start)
	}
	v.SetTotal(100)
	if start, end := v.Window(); start != 90 || end != 100 {
		t.Fatalf("follow window = [%d,%d), want [90,100)", start, end)
	}
	if !v.Follow {
		t.Fatal("follow dropped after growth")
	}
}

// TestViewportLineScroll checks the line moves used by ctrl+y/e, ctrl+p/n and the
// wheel, including the re-follow when the bottom is reached again.
func TestViewportLineScroll(t *testing.T) {
	v := Viewport{Follow: true}
	v.Resize(10)
	v.SetTotal(100)

	v.LineUp()
	if v.Follow || v.Top != 89 {
		t.Fatalf("lineUp: top=%d follow=%v", v.Top, v.Follow)
	}
	v.ScrollLines(-3)
	if v.Top != 86 {
		t.Fatalf("scrollLines(-3): top=%d", v.Top)
	}
	v.LineDown()
	if v.Top != 87 || v.Follow {
		t.Fatalf("lineDown: top=%d follow=%v", v.Top, v.Follow)
	}
	v.ScrollLines(1000)
	if !v.Follow || v.Top != 90 {
		t.Fatalf("scroll to bottom: top=%d follow=%v", v.Top, v.Follow)
	}
}

// TestViewportPage checks full-page moves and clamping at both ends.
func TestViewportPage(t *testing.T) {
	v := Viewport{Follow: true}
	v.Resize(10)
	v.SetTotal(100)

	v.PageUp()
	if v.Top != 80 || v.Follow {
		t.Fatalf("pageUp: top=%d follow=%v", v.Top, v.Follow)
	}
	for i := 0; i < 20; i++ {
		v.PageUp()
	}
	if v.Top != 0 || !v.AtTop() {
		t.Fatalf("pageUp clamp: top=%d atTop=%v", v.Top, v.AtTop())
	}
	v.PageDown()
	if v.Top != 10 || v.Follow {
		t.Fatalf("pageDown: top=%d follow=%v", v.Top, v.Follow)
	}
	for i := 0; i < 20; i++ {
		v.PageDown()
	}
	if !v.Follow || v.Top != 90 {
		t.Fatalf("pageDown clamp: top=%d follow=%v", v.Top, v.Follow)
	}
}

// TestViewportShrinkClamps checks a resize that hides lines above does not leave
// top past the new maximum, and that offset agrees with window.
func TestViewportShrinkClamps(t *testing.T) {
	v := Viewport{Follow: false}
	v.Resize(10)
	v.SetTotal(50)
	v.Top = 40
	v.Resize(20)
	if v.Top != 30 {
		t.Fatalf("shrink clamp: top=%d, want 30", v.Top)
	}
	above, below := v.Offset()
	if above != 30 || below != 0 {
		t.Fatalf("offset = %d/%d, want 30/0", above, below)
	}
	if first := v.First(); first != 30 {
		t.Fatalf("first = %d, want 30", first)
	}
}

// TestViewportEmpty checks every move is safe with no content.
func TestViewportEmpty(t *testing.T) {
	v := Viewport{Follow: true}
	v.Resize(0)
	v.SetTotal(0)
	start, end := v.Window()
	if start != 0 || end != 0 {
		t.Fatalf("empty window = [%d,%d)", start, end)
	}
	v.PageUp()
	v.PageDown()
	v.LineUp()
	v.LineDown()
	v.FollowBottom()
	if !v.Follow || !v.AtTop() {
		t.Fatalf("empty after moves: top=%d follow=%v", v.Top, v.Follow)
	}
}
