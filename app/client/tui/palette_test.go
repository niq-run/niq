package tui

import (
	"context"
	"strings"
	"testing"
)

// TestSlashCommandOpensPalette pins that a confirmed /command consumes the draft
// and raises the inline list on its current value.
func TestSlashCommandOpensPalette(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "niq", value: "niq", note: "reason"}}

	c.in.setDraft("/view")
	c.handleKey(context.Background(), press{Kind: pressEnter}, nil)

	if c.palette != menuView {
		t.Fatalf("/view opened palette %v, want menuView", c.palette)
	}
	if got := c.in.text(); got != "" {
		t.Fatalf("the draft survived the command: %q", got)
	}
	// The current value ("all", value "") is the opening row.
	if c.paletteCur != 0 {
		t.Fatalf("palette opened on row %d, want 0", c.paletteCur)
	}
}

// TestSlashCommandNotSent pins that a command is not sent as a message.
func TestSlashCommandNotSent(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "niq", value: "niq", note: "reason"}}
	c.sends = make(chan sendResult, 1)

	c.in.setDraft("/help")
	c.handleKey(context.Background(), press{Kind: pressEnter}, nil)

	if !c.help {
		t.Fatal("/help did not open the keymap")
	}
	select {
	case <-c.sends:
		t.Fatal("the command was sent as a message")
	default:
	}
}

// TestPaletteMoveWraps pins that j/k and the arrows wrap around the list.
func TestPaletteMoveWraps(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "a", value: "a"}, {label: "b", value: "b"}}
	c.openPalette(context.Background(), menuView)
	n := len(c.itemsFor(menuView))
	if n != 3 {
		t.Fatalf("view list has %d rows, want 3", n)
	}
	c.movePalette(-1)
	if c.paletteCur != n-1 {
		t.Fatalf("movePalette(-1) from 0 gave %d, want %d", c.paletteCur, n-1)
	}
	c.movePalette(+1)
	if c.paletteCur != 0 {
		t.Fatalf("movePalette(+1) wrapped to %d, want 0", c.paletteCur)
	}
}

// TestPaletteApplySetsFilter pins that committing a row applies it and closes the
// list.
func TestPaletteApplySetsFilter(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "niq", value: "niq"}}
	c.openPalette(context.Background(), menuView)
	c.paletteCur = 1 // the "niq" scope
	c.applyPalette(context.Background())

	if c.palette != menuNone {
		t.Fatalf("palette still open after applying (%v)", c.palette)
	}
	if c.filter != "niq" {
		t.Fatalf("filter is %q, want niq", c.filter)
	}
}

// TestPaletteEscCancels pins that esc closes the list without applying.
func TestPaletteEscCancels(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "niq", value: "niq"}}
	c.openPalette(context.Background(), menuView)
	c.paletteCur = 1

	c.handleKey(context.Background(), press{Kind: pressEsc}, nil)

	if c.palette != menuNone {
		t.Fatal("esc did not close the palette")
	}
	if c.filter != "" {
		t.Fatalf("esc applied a choice: filter=%q", c.filter)
	}
}

// TestPaletteRendersUnderComposer pins that an open palette adds option rows and
// the operation hint to the dock, and takes height from the transcript.
func TestPaletteRendersUnderComposer(t *testing.T) {
	c, buf := newTestChat(t)
	c.targets = []menuItem{{label: "alpha", value: "alpha"}, {label: "beta", value: "beta"}}
	c.openPalette(context.Background(), menuView)

	before := c.viewHeight()
	c.render()

	rows := strings.Split(stripANSI(buf.String()), "\n")
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "all") || !strings.Contains(joined, "alpha") {
		t.Fatalf("palette options missing from the frame:\n%s", joined)
	}
	if !strings.Contains(joined, "esc cancel") {
		t.Fatalf("operation hint missing from the frame:\n%s", joined)
	}
	if c.viewHeight() != before {
		t.Fatalf("viewHeight changed on render: %d -> %d", before, c.viewHeight())
	}
	if before >= c.height-4 {
		t.Fatalf("palette did not raise the composer (viewHeight %d)", before)
	}
}

// TestCommandMenuFilters pins input-as-filter: '/' opens the whole list, a
// partial token narrows it, no match hides it, and a space makes it a message.
func TestCommandMenuFilters(t *testing.T) {
	c, _ := newTestChat(t)

	c.in.setDraft("/")
	if n := len(c.commandList()); n != len(commandItems()) {
		t.Fatalf("bare '/' shows %d commands, want %d", n, len(commandItems()))
	}

	c.in.setDraft("/mo")
	got := c.commandList()
	if len(got) != 2 || got[0].value != "mode" || got[1].value != "model" {
		t.Fatalf("/mo filtered to %+v, want mode+model", got)
	}

	c.in.setDraft("/he")
	if got := c.commandList(); len(got) != 1 || got[0].value != "help" {
		t.Fatalf("/he filtered to %+v, want just help", got)
	}

	c.in.setDraft("/zzz")
	if c.commandList() != nil {
		t.Fatal("a token with no match should hide the menu")
	}

	c.in.setDraft("/view scope")
	if c.commandList() != nil {
		t.Fatal("a draft with a space is a message, not a command")
	}
}

// TestCommandMenuTabCommits pins that Tab commits the highlighted (here the
// first, since /vi matches both /view and /vim) command.
func TestCommandMenuTabCommits(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "niq", value: "niq", note: "reason"}}
	c.in.setDraft("/vi")

	c.handleKey(context.Background(), press{Kind: pressTab}, nil)

	if c.palette != menuView {
		t.Fatalf("tab on /vi opened palette %v, want menuView", c.palette)
	}
	if got := c.in.text(); got != "" {
		t.Fatalf("the draft survived the command: %q", got)
	}
}

// TestCommandMenuNav pins that the arrows (and ctrl+p/n) move the highlight and
// that Enter commits the highlighted row, not always the first.
func TestCommandMenuNav(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "niq", value: "niq", note: "reason"}}
	c.in.setDraft("/vi") // /view then /vim

	c.handleKey(context.Background(), press{Kind: pressDown}, nil)
	if c.cmdCur != 1 {
		t.Fatalf("down moved to %d, want 1", c.cmdCur)
	}
	c.handleKey(context.Background(), press{Kind: pressEnter}, nil)
	if c.normal != true {
		t.Fatalf("committing /vim did not enter normal mode (palette=%v)", c.palette)
	}
}

// TestCommandMenuEscCancels pins that esc clears the draft and keeps insert mode.
func TestCommandMenuEscCancels(t *testing.T) {
	c, _ := newTestChat(t)
	c.in.setDraft("/vi")

	c.handleKey(context.Background(), press{Kind: pressEsc}, nil)

	if got := c.in.text(); got != "" {
		t.Fatalf("esc left the draft as %q", got)
	}
	if c.normal {
		t.Fatal("esc on the command menu entered normal mode instead of cancelling")
	}
}

// TestCommandMenuRendersInline pins that typing a partial command shows the
// filtered command list and its hint under the composer.
func TestCommandMenuRendersInline(t *testing.T) {
	c, buf := newTestChat(t)
	c.in.setDraft("/vi")

	if c.inlineDockRows() == 0 {
		t.Fatal("the command menu did not reserve dock rows")
	}
	c.render()

	frame := stripANSI(buf.String())
	for _, want := range []string{"/view", "/vim", "tab select"} {
		if !strings.Contains(frame, want) {
			t.Errorf("command menu frame is missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "/target") {
		t.Error("a filtered menu still shows a non-matching command (/target)")
	}
}

// TestCommandHintIndentAndBlank pins the hint layout: it is set apart from the
// list by a blank row and indented two cells to line up with the option labels,
// instead of hugging the left edge.
func TestCommandHintIndentAndBlank(t *testing.T) {
	c, buf := newTestChat(t)
	c.in.setDraft("/vi") // /view and /vim

	// Two option rows, plus a blank row and the hint.
	if got := c.inlineDockRows(); got != 4 {
		t.Fatalf("dock reserves %d rows for 2 options + blank + hint, want 4", got)
	}
	c.render()

	// Construct the expected hint row the same way the dock does, so the check is
	// independent of whether colour is on.
	want := "\x1b[2K  " + dim("↑/↓ move · tab select · esc cancel")
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("hint is not 2-space indented under the labels; raw buffer:\n%q", buf.String())
	}
}
