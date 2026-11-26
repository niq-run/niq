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
func (c *chat) menuItems() []menuItem { return c.itemsFor(c.menu) }

// itemsFor is the rows of the list of kind k — shared by the modal menu and the
// inline slash-command palette.
func (c *chat) itemsFor(k menuKind) []menuItem {
	switch k {
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
	c.applyChoice(ctx, c.menu, items[c.menuCur])
	c.closeMenu()
}

// applyChoice applies one row of a list of kind k — shared by the modal menu and
// the inline slash-command palette.
func (c *chat) applyChoice(ctx context.Context, k menuKind, it menuItem) {
	switch k {
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
}

// windowRange is the slice [start,end) of n rows to show around cur so at most
// fit rows appear, keeping the highlight visible and the window in bounds.
func windowRange(n, cur, fit int) (start, end int) {
	if fit < 1 {
		fit = 1
	}
	if n <= fit {
		return 0, n
	}
	start = cur - fit/2
	if start < 0 {
		start = 0
	}
	if start+fit > n {
		start = n - fit
	}
	return start, start + fit
}

// optionBody renders one window [start,end) of a list, the highlighted row in
// reverse video, every label padded to the widest so the notes line up.
func optionBody(items []menuItem, cur, start, end int) []string {
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
		if i == cur {
			body = append(body, reverse("› "+it.label+pad)+" "+dim(it.note))
		} else {
			body = append(body, "  "+it.label+pad+" "+dim(it.note))
		}
	}
	return body
}

// menuCard lays the open list out as a card, windowing to the frame height so a
// long list scrolls with the highlight.
func (c *chat) menuCard() ([]string, int) {
	items := c.menuItems()
	fit := c.height - 2 - ui.CardOverhead // the card's borders and its frame
	if fit < 1 {
		fit = 1
	}
	start, end := windowRange(len(items), c.menuCur, fit)
	// Remember the window so a click can map a screen row back to an item.
	c.menuStart, c.menuCount = start, end-start
	body := optionBody(items, c.menuCur, start, end)
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

// ── Slash-command palette ──
//
// A slash command (/target, /view, /model, …) is confirmed in the composer, then
// its options are shown inline under the input instead of in the centred card.
// The palette shares itemsFor/applyChoice with the modal menu; it only differs in
// where and how it renders (dock rows, no border).

// maxPaletteRows caps the inline list so a long model list still leaves the input
// and its hint visible.
const maxPaletteRows = 8

// openPalette raises the inline list of kind k on its current value, fetching the
// model list first when k is the model picker.
func (c *chat) openPalette(ctx context.Context, k menuKind) {
	if k == menuModel {
		w := c.contextWorker()
		if w == "" {
			c.flash("no worker to switch")
			return
		}
		c.modelWorker = w
		c.startProviders(ctx, w, c.provResults)
	}
	c.palette = k
	c.paletteCur = c.menuIndex(k)
}

func (c *chat) closePalette() { c.palette = menuNone; c.paletteCur = 0 }

// movePalette steps the highlight, wrapping.
func (c *chat) movePalette(d int) {
	n := len(c.itemsFor(c.palette))
	if n == 0 {
		return
	}
	c.paletteCur = (c.paletteCur + d + n) % n
}

// applyPalette commits the highlighted row and closes the list.
func (c *chat) applyPalette(ctx context.Context) {
	items := c.itemsFor(c.palette)
	if c.paletteCur >= 0 && c.paletteCur < len(items) {
		c.applyChoice(ctx, c.palette, items[c.paletteCur])
	}
	c.closePalette()
	if c.in != nil {
		c.in.clear()
	}
}

// paletteView is the visible slice of the palette and how many rows it fills.
func (c *chat) paletteView() ([]string, int) {
	items := c.itemsFor(c.palette)
	start, end := windowRange(len(items), c.paletteCur, maxPaletteRows)
	return optionBody(items, c.paletteCur, start, end), end - start
}

// paletteRows is the number of option rows the palette adds to the dock.
func (c *chat) paletteRows() int {
	if c.palette == menuNone {
		return 0
	}
	start, end := windowRange(len(c.itemsFor(c.palette)), c.paletteCur, maxPaletteRows)
	return end - start
}

// inlineMenu is the list shown under the composer: the option palette when one is
// open, else the live command menu when the draft is a /command. keepCursor is
// true for the command menu, which is typed into, so the composer keeps the text
// cursor; the option palette holds a choice and hides it. ok is false when
// neither is shown.
func (c *chat) inlineMenu() (opts []string, footer string, keepCursor, ok bool) {
	if c.help || c.usagePanel || c.menu != menuNone {
		return nil, "", false, false
	}
	if c.palette != menuNone {
		opts, _ = c.paletteView()
		return opts, "↑/↓ move · enter select · esc cancel", false, true
	}
	m := c.commandList()
	if m == nil {
		return nil, "", false, false
	}
	if c.cmdCur < 0 || c.cmdCur >= len(m) {
		c.cmdCur = 0
	}
	start, end := windowRange(len(m), c.cmdCur, maxPaletteRows)
	return optionBody(m, c.cmdCur, start, end), "↑/↓ move · tab select · esc cancel", true, true
}

// inlineDockRows is how many rows inlineMenu adds to the dock (its rows, a blank
// row and the footer), or 0 when no list is shown.
func (c *chat) inlineDockRows() int {
	if c.help || c.usagePanel || c.menu != menuNone {
		return 0
	}
	if c.palette != menuNone {
		return c.paletteRows() + 2 // option rows + blank + hint
	}
	if m := c.commandList(); m != nil {
		start, end := windowRange(len(m), c.cmdCur, maxPaletteRows)
		return (end - start) + 2 // option rows + blank + hint
	}
	return 0
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
		{"type /", "command menu: type filters, tab picks"},
		{"ctrl+? / ?", "this help · ctrl+c/q leave"},
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
