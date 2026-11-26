// The modal lists over the conversation: the target picker (ctrl+t), the view
// picker (ctrl+v), the delivery-mode picker (ctrl+x) and the model picker, plus
// the keybinding page (ctrl+? / ?). The pickers are the same card with a
// different set of rows, so their state, navigation and layout live here; the
// help page is the same card with prose rows. dock.go paints the frame and
// keymap.go decodes the keys; the card geometry itself is in dialog.go.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/niq-run/niq/app/client/tui/ui"
	"github.com/niq-run/niq/app/standalone/contract"
)

// menuKind is the modal list raised over the chat, if any.
type menuKind int

const (
	menuNone menuKind = iota
	menuTarget
	menuView
	menuMode
	menuModel
)

// title is the card caption.
func (k menuKind) title() string {
	switch k {
	case menuTarget:
		return "target"
	case menuView:
		return "view"
	case menuMode:
		return "input mode"
	case menuModel:
		return "model"
	}
	return "menu"
}

// menuItem is one row of a modal list. value is what selecting it applies: a
// worker id, a delivery mode, or a vim mode.
type menuItem struct {
	label string
	value string
	note  string
}

// buildTargets is the target picker's rows: every talk partner. The set matches
// the WebUI's talk scope (reason, coding, niq), so a message can only be aimed at
// a worker that would answer it.
func buildTargets(workers []contract.Worker) []menuItem {
	var out []menuItem
	for _, w := range workers {
		if talkPartnerTypes[w.Type] {
			out = append(out, menuItem{label: workerName(w), value: w.ID, note: w.Type})
		}
	}
	return out
}

// viewItems is the view picker's rows: "all", then every talk partner.
func (c *chat) viewItems() []menuItem {
	out := make([]menuItem, 0, len(c.targets)+1)
	out = append(out, menuItem{label: "all", note: "every conversation"})
	return append(out, c.targets...)
}

// modeMenuItems is the delivery-mode picker's rows: the three server-side levels
// the WebUI offers (append / interrupt / schedule), then the unset default that
// lets the worker decide.
func modeMenuItems() []menuItem {
	return []menuItem{
		{label: "default", value: "", note: "the worker's own mode (append)"},
		{label: "append", value: "append", note: "no interrupt; respond next round"},
		{label: "interrupt", value: "interrupt", note: "cancel in-flight reasoning, handle now"},
		{label: "schedule", value: "schedule", note: "only when idle; least intrusive"},
	}
}

// menuItems is the open menu's rows.
func (c *chat) menuItems() []menuItem {
	switch c.menu {
	case menuTarget:
		return c.targets
	case menuView:
		return c.viewItems()
	case menuMode:
		return modeMenuItems()
	case menuModel:
		return c.modelItems()
	}
	return nil
}

// modelItems is the model picker's rows: every model of every provider the
// worker reports, tagged with its provider, current marked. Until the providers
// are fetched it shows a single placeholder row.
func (c *chat) modelItems() []menuItem {
	list, ok := c.providers[c.modelWorker]
	if !ok {
		return []menuItem{{label: "loading…", note: "fetching providers"}}
	}
	var out []menuItem
	for _, p := range list.Providers {
		for _, m := range p.Models {
			note := p.Name
			if p.Name == list.Current.Provider && m == list.Current.Model {
				note += " · current"
			}
			out = append(out, menuItem{label: m, note: note, value: p.Name + "\x1f" + m})
		}
	}
	if len(out) == 0 {
		return []menuItem{{label: "no models", note: "the worker reported none"}}
	}
	return out
}

// menuIndex is the row a menu should open on: the current value.
func (c *chat) menuIndex(k menuKind) int {
	switch k {
	case menuTarget:
		for i, it := range c.targets {
			if it.value == c.target {
				return i
			}
		}
	case menuView:
		for i, it := range c.viewItems() {
			if it.value == c.filter {
				return i
			}
		}
	case menuMode:
		if c.in == nil {
			return 0
		}
		for i, it := range modeMenuItems() {
			if it.value == c.in.mode {
				return i
			}
		}
	case menuModel:
		cur := c.providers[c.modelWorker].Current
		want := cur.Provider + "\x1f" + cur.Model
		for i, it := range c.modelItems() {
			if it.value == want {
				return i
			}
		}
	}
	return 0
}

// openMenu raises a modal list on its current value.
func (c *chat) openMenu(k menuKind) {
	c.menu = k
	c.menuCur = c.menuIndex(k)
}

// openModelMenu opens the model picker for the dock's bound worker, fetching the
// worker's providers on first use.
func (c *chat) openModelMenu(ctx context.Context) {
	w := c.contextWorker()
	if w == "" {
		c.status = "no worker to switch"
		return
	}
	c.modelWorker = w
	c.startProviders(ctx, w, c.provResults)
	c.openMenu(menuModel)
}

func (c *chat) closeMenu() { c.menu = menuNone }

// moveMenu steps the highlight, wrapping.
func (c *chat) moveMenu(d int) {
	n := len(c.menuItems())
	if n == 0 {
		c.closeMenu()
		return
	}
	c.menuCur = (c.menuCur + d + n) % n
}

// applyMenu commits the highlighted row and closes the list.
func (c *chat) applyMenu(ctx context.Context) {
	items := c.menuItems()
	if c.menuCur < 0 || c.menuCur >= len(items) {
		c.closeMenu()
		return
	}
	it := items[c.menuCur]
	switch c.menu {
	case menuTarget:
		if it.value != "" {
			c.target = it.value
		}
	case menuView:
		c.filter = it.value
		c.linesDirty = true
		c.vp.FollowBottom()
		c.sel.clear()
		c.status = ""
	case menuMode:
		if c.in != nil {
			c.in.mode = it.value
		}
	case menuModel:
		if parts := strings.SplitN(it.value, "\x1f", 2); len(parts) == 2 && c.modelWorker != "" {
			c.status = "switching model…"
			c.startSwitch(ctx, c.modelWorker, parts[0], parts[1], c.provResults)
		}
	}
	c.closeMenu()
}

// menuCard lays the open list out as a card, windowing to the frame height so a
// long list scrolls with the highlight.
func (c *chat) menuCard() ([]string, int) {
	items := c.menuItems()
	fit := c.height - 2 - ui.CardOverhead // the card's borders and its frame
	if fit < 1 {
		fit = 1
	}
	start := 0
	if len(items) > fit {
		start = c.menuCur - fit/2
		if start < 0 {
			start = 0
		}
		if start+fit > len(items) {
			start = len(items) - fit
		}
	}
	end := start + fit
	if end > len(items) {
		end = len(items)
	}
	// Remember the window so a click can map a screen row back to an item.
	c.menuStart, c.menuCount = start, end-start

	w := 0
	for _, it := range items {
		if lw := displayWidth(it.label); lw > w {
			w = lw
		}
	}
	body := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		it := items[i]
		pad := strings.Repeat(" ", w-displayWidth(it.label))
		if i == c.menuCur {
			// The selected row is a filled block (the label's blue turned into a
			// background), matching the rule's keyboard focus.
			body = append(body, reverse("› "+it.label+pad)+" "+dim(it.note))
		} else {
			body = append(body, "  "+it.label+pad+" "+dim(it.note))
		}
	}
	footer := "↑/↓ · enter · esc"
	if len(items) > fit {
		footer = fmt.Sprintf("%d/%d · ↑/↓ · enter · esc", c.menuCur+1, len(items))
	}
	return ui.Dialog{Title: c.menu.title(), Footer: footer, Body: body}.Lines(c.width, c.height)
}

// paintMenu overlays the open list on the frame and records its block, so a click
// outside it closes the list and a click on an option selects it.
func (c *chat) paintMenu(rows []string) {
	card, boxW := c.menuCard()
	c.cardRect = ui.PaintCard(rows, card, boxW, c.width)
	// The card's top border sits ui.FrameLineY+ui.FrameGapY rows into the framed block;
	// the first option is the row just under that border.
	c.menuTopY = c.cardRect.Y0 + ui.FrameLineY + ui.FrameGapY + 1
	c.menuX0 = c.cardRect.X0 + ui.FrameLineX + ui.FrameGapX
	c.menuX1 = c.cardRect.X1 - ui.FrameLineX - ui.FrameGapX
}

// ── Help page ──

// helpEntry is one line of the page: the keys and what they do.
type helpEntry struct{ keys, action string }

// helpSections is the content. "any mode" comes first: those are the shortcuts
// that open the popups, and keeping them at the top means a short terminal still
// shows them when the page has to be truncated.
var helpSections = []struct {
	title   string
	entries []helpEntry
}{
	{"any mode", []helpEntry{
		{"ctrl+t / ctrl+v", "target / view popups (click, or tab+enter)"},
		{"ctrl+x", "input mode popup (or click it)"},
		{"ctrl+? / ?", "this help (or click help)"},
		{"ctrl+c / ctrl+q", "leave"},
		{"wheel / drag", "scroll · select/copy · click buttons"},
	}},
	{"insert", []helpEntry{
		{"enter", "send the message"},
		{"shift+enter", "newline (ctrl+j, alt+enter too)"},
		{"ctrl+a/e, ctrl+b/f", "line start/end · char left/right"},
		{"ctrl+p / ctrl+n", "previous / next line"},
		{"ctrl+k/w, ctrl+y", "kill end/word · yank"},
		{"esc", "normal mode"},
	}},
	{"normal", []helpEntry{
		{"j/k, ctrl+e/y", "line down / up"},
		{"ctrl+n / ctrl+p", "next / previous message"},
		{"ctrl+d/u, ctrl+f/b", "page down / up"},
		{"g / G, i", "oldest/newest · insert mode"},
	}},
}

// helpCard lays the page out as a card. paintHelp places it over the frame.
func helpCard(width, maxRows int) ([]string, int) {
	return ui.Dialog{Title: "help", Footer: "esc or click outside", Body: helpBody()}.Lines(width, maxRows)
}

// paintHelp overlays the keybinding card on the finished frame and records the
// block it covers, so a click outside it can close the page.
func (c *chat) paintHelp(rows []string) {
	card, boxW := helpCard(c.width, c.height)
	c.cardRect = ui.PaintCard(rows, card, boxW, c.width)
}

// helpBody renders the sections to card rows, aligning the key column.
func helpBody() []string {
	var body []string
	for _, sec := range helpSections {
		body = append(body, bold(sec.title))
		for _, e := range sec.entries {
			keys := e.keys
			if w := displayWidth(keys); w > 18 {
				keys = truncateCells(keys, 18)
			} else {
				keys += strings.Repeat(" ", 18-w)
			}
			body = append(body, "  "+dim(keys)+" "+e.action)
		}
	}
	return body
}
