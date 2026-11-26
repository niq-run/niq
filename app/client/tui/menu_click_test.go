package tui

import (
	"context"
	"testing"
)

// TestMenuClickSelectsOption pins that a click on a popup's option row applies
// that option, and a click outside the card still just closes it.
func TestMenuClickSelectsOption(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{
		{label: "alpha", value: "alpha"},
		{label: "beta", value: "beta"},
		{label: "gamma", value: "gamma"},
	}
	c.target = "alpha"
	c.openMenu(menuTarget)
	c.render()

	if c.menuTopY <= 0 || c.menuCount != 3 {
		t.Fatalf("menu rows not recorded: top=%d count=%d", c.menuTopY, c.menuCount)
	}

	// Click the third option row (press coordinates are 1-based).
	row := c.menuTopY + 2
	c.handleKey(context.Background(), press{Kind: pressClick, X: c.menuX0 + 1, Y: row + 1}, nil)
	if c.menu != menuNone {
		t.Fatalf("menu still open after selecting an option (%v)", c.menu)
	}
	if c.target != "gamma" {
		t.Fatalf("clicking the third row selected %q, want gamma", c.target)
	}
}

// TestMenuClickOutsideCloses pins that a click away from the card dismisses the
// list without applying anything.
func TestMenuClickOutsideCloses(t *testing.T) {
	c, _ := newTestChat(t)
	c.targets = []menuItem{{label: "alpha", value: "alpha"}, {label: "beta", value: "beta"}}
	c.target = "alpha"
	c.openMenu(menuTarget)
	c.render()

	c.handleKey(context.Background(), press{Kind: pressClick, X: 1, Y: 1}, nil)
	if c.menu != menuNone {
		t.Fatal("click outside did not close the menu")
	}
	if c.target != "alpha" {
		t.Fatalf("click outside changed the target to %q", c.target)
	}
}
