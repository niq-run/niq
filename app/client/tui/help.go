// The keybinding page: `?` in normal mode, or ctrl+? in either mode, opens a
// bordered card listing both keymaps. The card is an overlay — it covers only the
// rows it needs, so the transcript around it stays readable. Its layout lives in
// dialog.go.
package tui

import "strings"

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
		{"wheel / drag", "scroll / select and copy"},
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
		{"ctrl+d/u, ctrl+f/b", "page down / up"},
		{"g / G", "oldest / newest"},
		{"i", "insert mode"},
	}},
}

// helpCard lays the page out as a card. paintHelp places it over the frame.
func helpCard(width, maxRows int) ([]string, int) {
	return dialog{title: "help", footer: "esc or click outside", body: helpBody()}.lines(width, maxRows)
}

// paintHelp overlays the keybinding card on the finished frame and records the
// block it covers, so a click outside it can close the page.
func (c *chat) paintHelp(rows []string) {
	card, boxW := helpCard(c.width, c.height)
	c.cardRect = paintCard(rows, card, boxW, c.width)
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
