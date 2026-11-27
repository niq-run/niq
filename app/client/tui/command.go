// The slash-command menu: type '/' in the composer and the command list opens
// inline under the input; keep typing and it filters live (input-as-filter), so
// "/mo" leaves /mode and /model. Tab or Enter commits the highlighted row —
// opening that command's option palette (see menu.go) or running a one-shot.
//
// This is the front door to the option palette; the option palette itself is the
// shared dock list in menu.go, driven by c.palette.
package tui

import (
	"context"
	"strings"
)

// commandItems is the menu: one row per clickable button on the status rule and
// the context line, so every button has a command and every command a button.
func commandItems() []menuItem {
	return []menuItem{
		{label: "/target", value: "target", note: "pick the worker to talk to"},
		{label: "/view", value: "view", note: "filter the conversation by worker"},
		{label: "/mode", value: "mode", note: "input delivery mode"},
		{label: "/model", value: "model", note: "switch the worker's model"},
		{label: "/usage", value: "usage", note: "context usage detail"},
		{label: "/help", value: "help", note: "keybindings"},
		{label: "/vim", value: "vim", note: "toggle insert / normal"},
	}
}

// commandToken is the partial command in the composer: a leading '/', then a
// single token with no space. A space or newline ends it — "/foo bar" is a
// message, not a command — and normal mode never shows the menu.
func (c *chat) commandToken() (string, bool) {
	if c.in == nil || c.normal {
		return "", false
	}
	txt := c.in.text()
	if !strings.HasPrefix(txt, "/") {
		return "", false
	}
	token := txt[1:]
	if strings.ContainsAny(token, " \n\t") {
		return "", false
	}
	return token, true
}

// commandList is the filtered command menu, or nil when the draft is not a
// command or nothing matches (so the menu hides and Enter sends the draft).
func (c *chat) commandList() []menuItem {
	token, ok := c.commandToken()
	if !ok {
		return nil
	}
	all := commandItems()
	if token == "" {
		return all
	}
	lt := strings.ToLower(token)
	out := make([]menuItem, 0, len(all))
	for _, it := range all {
		if strings.HasPrefix(strings.ToLower(strings.TrimPrefix(it.label, "/")), lt) {
			out = append(out, it)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// handleCommandKey consumes the keys the command menu owns — navigation, commit
// and cancel — and lets everything else (typing, editing) fall through so the
// composer keeps filtering the list.
func (c *chat) handleCommandKey(ctx context.Context, p press) bool {
	matches := c.commandList()
	if matches == nil {
		return false
	}
	n := len(matches)
	if c.cmdCur < 0 || c.cmdCur >= n {
		c.cmdCur = 0
	}
	switch p.Kind {
	case pressEsc:
		if c.in != nil {
			c.in.clear()
		}
		c.cmdCur = 0
		return true
	case pressUp, pressLineUp, pressPrevUser:
		c.cmdCur = (c.cmdCur - 1 + n) % n
		return true
	case pressDown, pressLineDown, pressNextUser:
		c.cmdCur = (c.cmdCur + 1) % n
		return true
	case pressTab, pressEnter:
		c.commitCommand(ctx, matches)
		return true
	}
	return false
}

// commitCommand runs the highlighted command: a picker opens its option palette,
// a one-shot runs at once. The composer is cleared either way.
func (c *chat) commitCommand(ctx context.Context, matches []menuItem) {
	idx := c.cmdCur
	if idx < 0 || idx >= len(matches) {
		idx = 0
	}
	name := matches[idx].value
	c.cmdCur = 0
	if c.in != nil {
		c.in.clear()
	}
	switch name {
	case "target":
		c.openPalette(ctx, menuTarget)
	case "view":
		c.openPalette(ctx, menuView)
	case "mode":
		c.openPalette(ctx, menuMode)
	case "model":
		c.openPalette(ctx, menuModel)
	case "help":
		c.help = true
	case "usage":
		c.openUsage()
	case "vim":
		c.normal = !c.normal
		c.sel.clear()
	}
}
