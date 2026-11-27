// Viewport is the transcript's vertical scroll window, as a standalone layout
// component.
//
// It owns nothing but scroll state — the content line count, the visible height,
// the first visible line, and whether it is pinned to the bottom. It knows nothing
// about events, workers, rendering or the keyboard, so its math can be tested and
// optimised on its own, and every scroll path (mouse wheel, page keys, vim-style
// line keys) funnels through the same handful of methods.
package layout

// Viewport is the scroll window over a list of content lines.
type Viewport struct {
	total  int  // number of content lines
	height int  // visible rows
	Top    int  // index of the first visible line
	Follow bool // pinned to the bottom
}

// Resize sets the visible height and re-clamps.
func (v *Viewport) Resize(height int) {
	if height < 1 {
		height = 1
	}
	v.height = height
	v.clamp()
}

// SetTotal updates the content length and re-clamps (following the bottom when
// pinned).
func (v *Viewport) SetTotal(total int) {
	if total < 0 {
		total = 0
	}
	v.total = total
	v.clamp()
}

// MaxTop is the largest valid first-line index.
func (v *Viewport) MaxTop() int {
	h := v.height
	if h < 1 {
		h = 1
	}
	if v.total <= h {
		return 0
	}
	return v.total - h
}

// clamp keeps Top valid; landing on the bottom re-enables following.
func (v *Viewport) clamp() {
	max := v.MaxTop()
	if v.Follow {
		v.Top = max
		return
	}
	if v.Top > max {
		v.Top = max
	}
	if v.Top < 0 {
		v.Top = 0
	}
	if v.Top == max {
		v.Follow = true
	}
}

// ScrollLines moves the window by n lines (negative is up). Any move unfollows;
// reaching the bottom again re-follows.
func (v *Viewport) ScrollLines(n int) {
	if n == 0 {
		return
	}
	v.Follow = false
	v.Top += n
	v.clamp()
}

func (v *Viewport) LineUp()   { v.ScrollLines(-1) }
func (v *Viewport) LineDown() { v.ScrollLines(1) }
func (v *Viewport) PageUp()   { v.ScrollLines(-v.VisibleHeight()) }
func (v *Viewport) PageDown() { v.ScrollLines(v.VisibleHeight()) }

// FollowBottom pins the window to the end again.
func (v *Viewport) FollowBottom() {
	v.Follow = true
	v.clamp()
}

// First is the index of the topmost visible line. Screen row r maps to content
// line First()+r.
func (v *Viewport) First() int { return v.Top }

// ToTop jumps to the oldest line in the window, leaving follow.
func (v *Viewport) ToTop() {
	v.Follow = false
	v.Top = 0
	v.clamp()
}

// ScrollTo jumps so line is the first visible line, leaving follow and clamping
// to the content.
func (v *Viewport) ScrollTo(line int) {
	v.Follow = false
	v.Top = line
	v.clamp()
}

// AtTop reports whether the first content line is visible (the cue to load more
// history).
func (v *Viewport) AtTop() bool { return v.Top == 0 }

// Window returns the [start, end) slice of content lines currently visible.
func (v *Viewport) Window() (start, end int) {
	h := v.VisibleHeight()
	start = v.Top
	if start > v.total {
		start = v.total
	}
	end = start + h
	if end > v.total {
		end = v.total
	}
	return start, end
}

// Offset reports how many lines sit above and below the window, for the scroll
// indicator.
func (v *Viewport) Offset() (above, below int) {
	_, end := v.Window()
	below = v.total - end
	if below < 0 {
		below = 0
	}
	return v.Top, below
}

// VisibleHeight is the number of visible rows, at least one.
func (v *Viewport) VisibleHeight() int {
	if v.height < 1 {
		return 1
	}
	return v.height
}
