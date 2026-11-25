package tui

import (
	"context"
	"testing"

	"github.com/niq-run/niq/app/standalone/contract"
)

// TestModelTabFocusOpensPicker checks the model on the context line is a Tab
// stop and that Enter on it opens the model picker.
func TestModelTabFocusOpensPicker(t *testing.T) {
	c, _ := newTestChat(t)
	c.partners = []contract.Worker{{ID: "reason", Type: "reason", Label: "ReasonBot"}}
	c.filter = "reason"
	c.render()

	ctx := context.Background()
	for i := 0; i < len(c.presentHints()); i++ {
		c.handleKey(ctx, press{kind: pressTab}, nil)
	}
	if c.focus != hintModelPick {
		t.Fatalf("focus = %v after cycling, want hintModelPick", c.focus)
	}

	c.handleKey(ctx, press{kind: pressEnter}, nil)
	if c.menu != menuModel {
		t.Fatalf("enter on the model opened menu %v, want menuModel", c.menu)
	}
}
