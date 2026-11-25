package tui

import "testing"

// TestViewportFollow pins the default: a viewport that follows keeps its window
// on the last screenful as content grows, and grows no scrollback above it.
func TestViewportFollow(t *testing.T) {
	v := viewport{follow: true}
	v.resize(10)
	v.setTotal(5)
	if start, _ := v.window(); start != 0 {
		t.Fatalf("short content start = %d, want 0", start)
	}
	v.setTotal(100)
	if start, end := v.window(); start != 90 || end != 100 {
		t.Fatalf("follow window = [%d,%d), want [90,100)", start, end)
	}
	if !v.follow {
		t.Fatal("follow dropped after growth")
	}
}

// TestViewportLineScroll checks the line moves used by ctrl+y/e, ctrl+p/n and the
// wheel, including the re-follow when the bottom is reached again.
func TestViewportLineScroll(t *testing.T) {
	v := viewport{follow: true}
	v.resize(10)
	v.setTotal(100)

	v.lineUp()
	if v.follow || v.top != 89 {
		t.Fatalf("lineUp: top=%d follow=%v", v.top, v.follow)
	}
	v.scrollLines(-3)
	if v.top != 86 {
		t.Fatalf("scrollLines(-3): top=%d", v.top)
	}
	v.lineDown()
	if v.top != 87 || v.follow {
		t.Fatalf("lineDown: top=%d follow=%v", v.top, v.follow)
	}
	v.scrollLines(1000)
	if !v.follow || v.top != 90 {
		t.Fatalf("scroll to bottom: top=%d follow=%v", v.top, v.follow)
	}
}

// TestViewportPage checks full-page moves and clamping at both ends.
func TestViewportPage(t *testing.T) {
	v := viewport{follow: true}
	v.resize(10)
	v.setTotal(100)

	v.pageUp()
	if v.top != 80 || v.follow {
		t.Fatalf("pageUp: top=%d follow=%v", v.top, v.follow)
	}
	for i := 0; i < 20; i++ {
		v.pageUp()
	}
	if v.top != 0 || !v.atTop() {
		t.Fatalf("pageUp clamp: top=%d atTop=%v", v.top, v.atTop())
	}
	v.pageDown()
	if v.top != 10 || v.follow {
		t.Fatalf("pageDown: top=%d follow=%v", v.top, v.follow)
	}
	for i := 0; i < 20; i++ {
		v.pageDown()
	}
	if !v.follow || v.top != 90 {
		t.Fatalf("pageDown clamp: top=%d follow=%v", v.top, v.follow)
	}
}

// TestViewportShrinkClamps checks a resize that hides lines above does not leave
// top past the new maximum, and that offset agrees with window.
func TestViewportShrinkClamps(t *testing.T) {
	v := viewport{follow: false}
	v.resize(10)
	v.setTotal(50)
	v.top = 40
	v.resize(20)
	if v.top != 30 {
		t.Fatalf("shrink clamp: top=%d, want 30", v.top)
	}
	above, below := v.offset()
	if above != 30 || below != 0 {
		t.Fatalf("offset = %d/%d, want 30/0", above, below)
	}
	if first := v.first(); first != 30 {
		t.Fatalf("first = %d, want 30", first)
	}
}

// TestViewportEmpty checks every move is safe with no content.
func TestViewportEmpty(t *testing.T) {
	v := viewport{follow: true}
	v.resize(0)
	v.setTotal(0)
	start, end := v.window()
	if start != 0 || end != 0 {
		t.Fatalf("empty window = [%d,%d)", start, end)
	}
	v.pageUp()
	v.pageDown()
	v.lineUp()
	v.lineDown()
	v.followBottom()
	if !v.follow || !v.atTop() {
		t.Fatalf("empty after moves: top=%d follow=%v", v.top, v.follow)
	}
}
