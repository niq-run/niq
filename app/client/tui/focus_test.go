package tui

import (
	"bufio"
	"context"
	"strings"
	"testing"
)

// TestTabDecoding pins the three spellings of Tab / Shift+Tab: the raw HT byte,
// xterm's CSI Z, and Kitty's `CSI 9 ; mod u`.
func TestTabDecoding(t *testing.T) {
	if p := decodePress(bufio.NewReader(strings.NewReader("")), '\t'); p.Kind != pressTab {
		t.Fatalf("HT decoded as %v, want pressTab", p.Kind)
	}
	if p := decodePress(bufio.NewReader(strings.NewReader("[Z")), 0x1b); p.Kind != pressBacktab {
		t.Fatalf("CSI Z decoded as %v, want pressBacktab", p.Kind)
	}
	if p := keyPress(9, 1); p.Kind != pressTab {
		t.Fatalf("kitty tab decoded as %v, want pressTab", p.Kind)
	}
	if p := keyPress(9, 2); p.Kind != pressBacktab {
		t.Fatalf("kitty shift+tab decoded as %v, want pressBacktab", p.Kind)
	}
}

// TestTabCyclesRuleButtons checks Tab and Shift+Tab walk the rule's buttons and
// the model in order, and wrap at both ends.
func TestTabCyclesRuleButtons(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "niq", value: "niq"}}
	c.render()

	ctx := context.Background()
	order := []hintAct{hintTargetPick, hintViewPick, hintInputModePick, hintVimPick, hintHelp, hintModelPick}
	for _, want := range order {
		c.handleKey(ctx, press{Kind: pressTab}, nil)
		if c.focus != want {
			t.Fatalf("tab focus = %v, want %v", c.focus, want)
		}
	}
	// One more Tab wraps back to the first button.
	c.handleKey(ctx, press{Kind: pressTab}, nil)
	if c.focus != hintTargetPick {
		t.Fatalf("wrap focus = %v, want hintTargetPick", c.focus)
	}
	// Shift+Tab steps backwards, wrapping from the first to the last (the model).
	c.handleKey(ctx, press{Kind: pressBacktab}, nil)
	if c.focus != hintModelPick {
		t.Fatalf("backtab focus = %v, want hintModelPick", c.focus)
	}
}

// TestEnterActivatesFocusedButton checks Enter on a focused button runs its
// action instead of sending: help opens the page, target opens its picker.
func TestEnterActivatesFocusedButton(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "niq", value: "niq"}}
	c.render()
	ctx := context.Background()

	c.focus = hintHelp
	c.handleKey(ctx, press{Kind: pressEnter}, nil)
	if !c.help {
		t.Fatal("enter on the help button did not open the help page")
	}
	if c.focus != hintNone {
		t.Fatalf("focus = %v after activating, want hintNone", c.focus)
	}
	c.help = false

	c.focus = hintTargetPick
	c.handleKey(ctx, press{Kind: pressEnter}, nil)
	if c.menu != menuTarget {
		t.Fatalf("enter on the target button opened menu %v, want menuTarget", c.menu)
	}
}

// TestTypingDropsFocus checks a normal key clears the button focus and goes back
// to editing, so the user never types into a dead screen.
func TestTypingDropsFocus(t *testing.T) {
	c, _ := newTestChat(t)
	c.render()
	c.focus = hintViewPick

	c.handleKey(context.Background(), press{Kind: pressRune, R: 'x'}, nil)
	if c.focus != hintNone {
		t.Fatalf("focus = %v after typing, want hintNone", c.focus)
	}
	if got := c.in.text(); got != "x" {
		t.Fatalf("typed rune lost: %q", got)
	}
}

// TestEscCancelsFocus checks Esc drops the button focus without leaving insert
// mode (a second Esc still enters normal mode).
func TestEscCancelsFocus(t *testing.T) {
	c, _ := newTestChat(t)
	c.render()
	c.focus = hintInputModePick

	c.handleKey(context.Background(), press{Kind: pressEsc}, nil)
	if c.focus != hintNone {
		t.Fatalf("focus = %v after esc, want hintNone", c.focus)
	}
	if c.normal {
		t.Fatal("the first esc left insert mode; it should only cancel focus")
	}
}
