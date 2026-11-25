// viewport is the transcript's vertical scroll window, as a standalone component.
//
// It owns nothing but scroll state — the content line count, the visible height,
// the first visible line, and whether it is pinned to the bottom. It knows nothing
// about events, workers, rendering or the keyboard, so its math can be tested and
// optimised on its own, and every scroll path (mouse wheel, page keys, vim-style
// line keys) funnels through the same handful of methods.
package tui

type viewport struct {
	total  int  // number of content lines
	height int  // visible rows
	top    int  // index of the first visible line
	follow bool // pinned to the bottom
}

// resize sets the visible height and re-clamps.
func (v *viewport) resize(height int) {
	if height < 1 {
		height = 1
	}
	v.height = height
	v.clamp()
}

// setTotal updates the content length and re-clamps (following the bottom when
// pinned).
func (v *viewport) setTotal(total int) {
	if total < 0 {
		total = 0
	}
	v.total = total
	v.clamp()
}

// maxTop is the largest valid first-line index.
func (v *viewport) maxTop() int {
	h := v.height
	if h < 1 {
		h = 1
	}
	if v.total <= h {
		return 0
	}
	return v.total - h
}

// clamp keeps top valid; landing on the bottom re-enables following.
func (v *viewport) clamp() {
	max := v.maxTop()
	if v.follow {
		v.top = max
		return
	}
	if v.top > max {
		v.top = max
	}
	if v.top < 0 {
		v.top = 0
	}
	if v.top == max {
		v.follow = true
	}
}

// scrollLines moves the window by n lines (negative is up). Any move unfollows;
// reaching the bottom again re-follows.
func (v *viewport) scrollLines(n int) {
	if n == 0 {
		return
	}
	v.follow = false
	v.top += n
	v.clamp()
}

func (v *viewport) lineUp()   { v.scrollLines(-1) }
func (v *viewport) lineDown() { v.scrollLines(1) }
func (v *viewport) pageUp()   { v.scrollLines(-v.visibleHeight()) }
func (v *viewport) pageDown() { v.scrollLines(v.visibleHeight()) }

// followBottom pins the window to the end again.
func (v *viewport) followBottom() {
	v.follow = true
	v.clamp()
}

// first is the index of the topmost visible line. Screen row r maps to content
// line first()+r.
func (v *viewport) first() int { return v.top }

// toTop jumps to the oldest line in the window, leaving follow.
func (v *viewport) toTop() {
	v.follow = false
	v.top = 0
	v.clamp()
}

// atTop reports whether the first content line is visible (the cue to load more
// history).
func (v *viewport) atTop() bool { return v.top == 0 }

// window returns the [start, end) slice of content lines currently visible.
func (v *viewport) window() (start, end int) {
	h := v.visibleHeight()
	start = v.top
	if start > v.total {
		start = v.total
	}
	end = start + h
	if end > v.total {
		end = v.total
	}
	return start, end
}

// offset reports how many lines sit above and below the window, for the scroll
// indicator.
func (v *viewport) offset() (above, below int) {
	_, end := v.window()
	below = v.total - end
	if below < 0 {
		below = 0
	}
	return v.top, below
}

func (v *viewport) visibleHeight() int {
	if v.height < 1 {
		return 1
	}
	return v.height
}
