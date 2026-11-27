package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/contract"
)

// usageEventBreakdown is a reason.response carrying a composition breakdown.
func usageEventBreakdown(worker string) contract.Event {
	return contract.Event{
		Type:     "reason.response",
		WorkerID: worker,
		Payload: map[string]any{
			"content": []any{"hi"},
			"meta": map[string]any{
				"usage": map[string]any{
					"input_tokens":          1020,
					"output_tokens":         40,
					"total_tokens":          1020,
					"context_window":        2000,
					"cache_read_tokens":     300,
					"cache_creation_tokens": 50,
					"cache_hit_percent":     30.0,
					"breakdown": []any{
						map[string]any{"key": "system", "tokens": 200},
						map[string]any{"key": "tools.builtin", "tokens": 300},
						map[string]any{"key": "tools.program", "tokens": 100},
						map[string]any{"key": "messages.user", "tokens": 300},
						map[string]any{"key": "messages.assistant", "tokens": 80},
						map[string]any{"key": "output", "tokens": 40},
					},
				},
			},
		},
	}
}

func TestUsageGroups(t *testing.T) {
	u, ok := parseUsage(usageEventBreakdown("reason").Payload)
	if !ok {
		t.Fatal("usage not parsed")
	}
	if len(u.breakdown) != 6 {
		t.Fatalf("breakdown sections = %d, want 6", len(u.breakdown))
	}
	want := map[string]int{"system": 200, "tools": 400, "messages": 380, "output": 40}
	got := map[string]int{}
	for _, g := range u.groups() {
		got[g.label] = g.tokens
	}
	for label, tokens := range want {
		if got[label] != tokens {
			t.Fatalf("%s = %d, want %d (all: %+v)", label, got[label], tokens, got)
		}
	}
}

// TestTabFocusOpensUsage checks the context percentage is a Tab stop and Enter
// opens the detail panel.
func TestTabFocusOpensUsage(t *testing.T) {
	c, _ := newTestChat(t)
	c.partners = []contract.Worker{{ID: "reason", Type: "reason", Label: "ReasonBot"}}
	c.events = []contract.Event{usageEventBreakdown("reason")}
	c.render()

	ctx := context.Background()
	reached := false
	for i := 0; i < len(c.presentHints()); i++ {
		c.handleKey(ctx, press{Kind: pressTab}, nil)
		if c.focus == hintUsagePick {
			reached = true
			break
		}
	}
	if !reached {
		t.Fatalf("tab never focused the percentage (focus=%v)", c.focus)
	}

	c.handleKey(ctx, press{Kind: pressEnter}, nil)
	if !c.usagePanel {
		t.Fatal("enter on the focused percentage did not open the usage panel")
	}
}
func TestContextPctClickOpensUsage(t *testing.T) {
	c, _ := newTestChat(t)
	c.partners = []contract.Worker{{ID: "reason", Type: "reason", Label: "ReasonBot"}}
	c.events = []contract.Event{usageEventBreakdown("reason")}
	c.render()

	if c.ctxPctX0 < 0 || c.ctxPctX1 <= c.ctxPctX0 {
		t.Fatalf("no percentage region recorded: [%d,%d)", c.ctxPctX0, c.ctxPctX1)
	}
	c.handleKey(context.Background(), press{Kind: pressClick, X: c.ctxPctX0 + 1, Y: c.ctxRow + 1}, nil)
	if !c.usagePanel {
		t.Fatal("clicking the percentage did not open the usage panel")
	}
	if c.usageWorker != "reason" {
		t.Fatalf("usage panel bound to %q, want reason", c.usageWorker)
	}

	// The card carries the headline, grouped rows and the cache footer.
	card, _ := c.usageCard()
	joined := strings.Join(card, "\n")
	for _, want := range []string{"ReasonBot", "51%", "system", "tools", "messages", "output", "cache 30%"} {
		if !strings.Contains(stripANSI(joined), want) {
			t.Fatalf("usage card missing %q:\n%s", want, stripANSI(joined))
		}
	}

	// Any key closes it.
	c.handleKey(context.Background(), press{Kind: pressRune, R: 'x'}, nil)
	if c.usagePanel {
		t.Fatal("a keypress did not close the usage panel")
	}
}
