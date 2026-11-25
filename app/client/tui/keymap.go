// The view's key semantics: what a decoded keypress means in each mode. The
// decoding itself is in keys.go — this half only talks to the chat.
package tui

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"
)

// handleKey applies one keystroke. It returns true when the user asked to leave.
func (c *chat) handleKey(ctx context.Context, p press, histories chan<- historyResult) bool {
	if c.help {
		// The help page is dismissed by any key except a click inside it: closing a
		// page should never tear down the conversation. Pressing ctrl+c twice (once to
		// close, once to leave) is the cost of that, and it beats losing the chat.
		switch p.kind {
		case pressTerminal:
			c.reportTerminal(p)
			return false
		case pressPaste, pressWheelUp, pressWheelDown, pressDrag, pressRelease, pressOther:
			return false
		case pressClick:
			if c.cardRect.contains(p.x-1, p.y-1) {
				return false
			}
		}
		c.help = false
		return false
	}
	if c.menu != menuNone {
		if p.kind == pressClick {
			x, y := p.x-1, p.y-1
			// A click outside the card closes the list, like every other popup.
			if !c.cardRect.contains(x, y) {
				c.closeMenu()
				return false
			}
			// A click on an option row selects and applies it, the way a click on a
			// menu item does everywhere else.
			if y >= c.menuTopY && y < c.menuTopY+c.menuCount && x >= c.menuX0 && x < c.menuX1 {
				if idx := c.menuStart + (y - c.menuTopY); idx >= 0 && idx < len(c.menuItems()) {
					c.menuCur = idx
					c.applyMenu(ctx)
				}
			}
			return false
		}
		if len(c.menuItems()) == 0 {
			c.closeMenu()
			return false
		}
		switch {
		case p.kind == pressInterrupt:
			return true
		case p.kind == pressEsc:
			c.closeMenu()
		case (p.kind == pressTarget && c.menu == menuTarget) ||
			(p.kind == pressView && c.menu == menuView) ||
			(p.kind == pressMode && c.menu == menuMode):
			// The key that opened a list closes it again.
			c.closeMenu()
		case p.kind == pressUp || (p.kind == pressRune && p.r == 'k'):
			c.moveMenu(-1)
		case p.kind == pressDown || (p.kind == pressRune && p.r == 'j'):
			c.moveMenu(+1)
		case p.kind == pressTab:
			c.moveMenu(+1)
		case p.kind == pressBacktab:
			c.moveMenu(-1)
		case p.kind == pressEnter:
			c.applyMenu(ctx)
		}
		return false
	}

	// Tab / Shift+Tab move keyboard focus across the rule's clickable buttons; while
	// one is focused, Enter activates it (instead of sending or scrolling). Any
	// other key drops the focus and is handled normally, so typing resumes.
	switch p.kind {
	case pressTab:
		c.cycleFocus(+1)
		return false
	case pressBacktab:
		c.cycleFocus(-1)
		return false
	}
	if c.focus != hintNone {
		switch p.kind {
		case pressEsc:
			c.focus = hintNone
			return false
		case pressEnter:
			act := c.focus
			c.focus = hintNone
			if act == hintModelPick {
				c.openModelMenu(ctx)
			} else {
				c.activateHint(act)
			}
			return false
		}
		c.focus = hintNone
	}

	switch p.kind {
	case pressInterrupt:
		return true
	case pressEsc:
		// Esc never quits (an IME cancelling a composition sends one). It enters
		// normal mode, where the keys scroll instead of typing.
		if c.sel.active {
			c.sel.clear()
		}
		c.normal = true
	case pressTarget:
		if len(c.targets) == 0 {
			c.status = "no talk partner"
			return false
		}
		c.openMenu(menuTarget)
		return false
	case pressWheelUp:
		c.vp.scrollLines(-wheelStep)
		c.maybeLoadOlder(ctx, histories)
	case pressWheelDown:
		c.vp.scrollLines(wheelStep)
	case pressClick:
		// Every segment of the rule is a popup: target, view, input mode, vim mode
		// and help. The model on the context line under the composer is a popup too
		// (the model picker). Anywhere else a click clears the old selection and
		// picks the mode by where it landed: the transcript is normal, the composer
		// insert.
		c.sel.clear()
		if y, x := p.y-1, p.x-1; y == c.ctxRow && x >= c.ctxModelX0 && x < c.ctxModelX1 {
			c.openModelMenu(ctx)
			return false
		}
		if act, ok := c.hintAction(p.y-1, p.x-1); ok {
			c.focus = hintNone
			c.activateHint(act)
			return false
		}
		if pt, ok := c.cellAt(p.y-1, p.x-1); ok {
			c.sel.begin(pt)
			c.normal = true
		} else {
			c.normal = false
		}
	case pressDrag:
		c.dragTo(ctx, p.y-1, p.x-1, histories)
	case pressRelease:
		if !c.sel.active {
			return false
		}
		if !c.sel.dragging {
			c.sel.clear()
			return false
		}
		if text := selectionText(c.lines, &c.sel); text != "" {
			c.scr.copyToClipboard(text)
			c.status = fmt.Sprintf("copied %d chars", utf8.RuneCountInString(text))
		}
	case pressPaste:
		c.in.paste(p.text)
	case pressTerminal:
		c.reportTerminal(p)
	case pressMode:
		c.openMenu(menuMode)
		return false
	case pressView:
		c.openMenu(menuView)
		return false
	}

	if c.normal {
		c.normalKey(ctx, p, histories)
		return false
	}
	c.insertKey(ctx, p, histories)
	return false
}

// reportTerminal applies a terminal colour/scheme report. A change means every
// painted line is stale, so the transcript is rebuilt.
func (c *chat) reportTerminal(p press) {
	if terminalReport(p.text) {
		c.linesDirty = true
	}
}

// activateHint runs the action behind a status-rule button: open its picker,
// open the help page, or toggle the vim mode. Shared by a mouse click and by
// Enter on a keyboard-focused button.
func (c *chat) activateHint(act hintAct) {
	switch act {
	case hintHelp:
		c.help = true
	case hintTargetPick:
		if len(c.targets) == 0 {
			c.status = "no talk partner"
			return
		}
		c.openMenu(menuTarget)
	case hintViewPick:
		c.openMenu(menuView)
	case hintInputModePick:
		c.openMenu(menuMode)
	case hintVimPick:
		// insert/normal is a two-state toggle, so it flips directly.
		c.normal = !c.normal
		c.sel.clear()
	}
}

// insertKey applies a keypress in insert mode: text and editing, plus the send.
// Paging stays available (ctrl+u/d are the classic pair), so the composer keeps
// the reading keys too.
func (c *chat) insertKey(ctx context.Context, p press, histories chan<- historyResult) {
	switch p.kind {
	case pressEnter:
		c.sendDraft(ctx)
	case pressPageUp:
		c.nudge(c.vp.pageUp, -1)
		c.maybeLoadOlder(ctx, histories)
	case pressPageDown:
		c.nudge(c.vp.pageDown, +1)
	case pressLineUp:
		c.nudge(c.vp.lineUp, -1)
		c.maybeLoadOlder(ctx, histories)
	case pressLineDown:
		c.nudge(c.vp.lineDown, +1)
	case pressNewline:
		c.in.ed.newline()
	case pressRune:
		c.in.insert(p.r)
	case pressBackspace:
		c.in.ed.backspace()
	case pressDelete:
		c.in.ed.delete()
	case pressLeft:
		c.in.ed.left()
	case pressRight:
		c.in.ed.right()
	case pressUp:
		c.in.ed.up()
	case pressDown:
		c.in.ed.down()
	case pressHome:
		c.in.ed.home()
	case pressEnd:
		c.in.ed.end()
	case pressKillEnd:
		c.in.killEnd()
	case pressKillWord:
		c.in.killWord()
	case pressYank:
		c.in.yank()
	case pressHelp:
		c.help = true
	case pressAlt:
		switch p.r {
		case 'b':
			c.in.ed.wordLeft()
		case 'f':
			c.in.ed.wordRight()
		}
	}
}

// normalKey applies a keypress in normal mode: the transcript has the keyboard,
// so keys scroll and navigate, and nothing types. This is the vim half of the two
// modes; insertKey is the other.
func (c *chat) normalKey(ctx context.Context, p press, histories chan<- historyResult) {
	switch p.kind {
	case pressEnter, pressDown, pressLineDown:
		c.nudge(c.vp.lineDown, +1)
	case pressRune:
		switch p.r {
		case 'j':
			c.nudge(c.vp.lineDown, +1)
		case 'k':
			c.nudge(c.vp.lineUp, -1)
			c.maybeLoadOlder(ctx, histories)
		case 'g':
			c.vp.toTop()
			c.maybeLoadOlder(ctx, histories)
		case 'G':
			c.vp.followBottom()
		case 'i', 'a':
			c.normal = false
		case '?':
			c.help = true
		default:
			// A key with no binding still has to say something: otherwise a user who
			// landed here by accident types into a dead screen.
			c.flash("normal mode · press i to type")
		}
	case pressUp, pressLineUp:
		c.nudge(c.vp.lineUp, -1)
		c.maybeLoadOlder(ctx, histories)
	case pressPageUp, pressLeft: // ctrl+u / ctrl+b / pgup
		c.nudge(c.vp.pageUp, -1)
		c.maybeLoadOlder(ctx, histories)
	case pressPageDown, pressRight: // ctrl+d / ctrl+f / pgdn
		c.nudge(c.vp.pageDown, +1)
	case pressHome: // ctrl+a
		c.vp.toTop()
		c.maybeLoadOlder(ctx, histories)
	case pressEnd: // ctrl+e, as in vim: one line down
		c.nudge(c.vp.lineDown, +1)
	case pressYank: // ctrl+y, as in vim: one line up
		c.nudge(c.vp.lineUp, -1)
		c.maybeLoadOlder(ctx, histories)
	case pressHelp: // ctrl+? (or ? below)
		c.help = true
	case pressAlt:
		// `esc` then `?` inside the 40 ms ESC window arrives as alt+?; normal mode has
		// no alt bindings, so read it as the help key the user meant to press.
		if p.r == '?' {
			c.help = true
		}
	}
}

// nudge runs one scroll move and, when it could not move the view, says so in the
// rule. A pager key that silently does nothing reads as a broken key, and the
// usual reason is that the whole transcript already fits on screen.
func (c *chat) nudge(move func(), dir int) {
	before := c.vp.first()
	move()
	if c.vp.first() != before {
		return
	}
	switch {
	case c.vp.maxTop() == 0:
		c.flash("everything fits the screen")
	case dir < 0:
		c.flash("top")
	default:
		c.flash("bottom")
	}
}

// flash puts a message in the rule for a couple of seconds. A plain status stays
// until something replaces it; this one clears itself, so scroll feedback does
// not pile up in the dock.
func (c *chat) flash(msg string) {
	c.status = msg
	c.statusUntil = time.Now().Add(2 * time.Second)
}

// hintKeys is the shortcut reminder, longest first: the widest one that still
// fits the rule is drawn, so a narrow terminal keeps a usable subset instead of
// losing the hints altogether.

// sendDraft sends what the composer holds and resets it. Delivery mode is a
// preference, not part of the message, so it survives.
func (c *chat) sendDraft(ctx context.Context) {
	if c.in.empty() {
		return
	}
	target := c.target
	draft := c.in
	next := newInput()
	next.mode = draft.mode
	c.in = next
	go func(d *input, to string) {
		err := c.send(ctx, d, to)
		select {
		case c.sends <- sendResult{text: d.text(), draft: d, err: err}:
		case <-ctx.Done():
		}
	}(draft, target)
}
