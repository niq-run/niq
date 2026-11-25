package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/contract"
)

// usageEvent builds a reason.response carrying meta.usage, the shape the reason
// worker attaches (see contextbreakdown.go).
func usageEvent(worker string) contract.Event {
	return contract.Event{
		Type:     "reason.response",
		WorkerID: worker,
		Payload: map[string]any{
			"content": []any{"hi"},
			"meta": map[string]any{
				"usage": map[string]any{
					"input_tokens":      100,
					"output_tokens":     20,
					"total_tokens":      120,
					"context_window":    1000,
					"cache_read_tokens": 60,
					"cache_hit_percent": 60.0,
				},
			},
		},
	}
}

func TestParseUsageAndFormatting(t *testing.T) {
	u, ok := parseUsage(usageEvent("reason").Payload)
	if !ok {
		t.Fatal("usage not parsed")
	}
	if u.total != 120 || u.contextWindow != 1000 || u.input != 100 || u.output != 20 || !u.hasCache {
		t.Fatalf("usage = %+v", u)
	}
	if got := u.occupancy(); got != "12% · 120/1K" {
		t.Fatalf("occupancy = %q", got)
	}
	// float64 (wire) and int (in-process) parse the same.
	wire, _ := parseUsage(map[string]any{"meta": map[string]any{"usage": map[string]any{
		"input_tokens": float64(100), "output_tokens": float64(20), "total_tokens": float64(120), "context_window": float64(1000),
	}}})
	if wire.total != 120 || wire.contextWindow != 1000 {
		t.Fatalf("wire parse = %+v", wire)
	}
	// No usage block → not ok.
	if _, ok := parseUsage(map[string]any{"content": []any{"x"}}); ok {
		t.Fatal("a response without meta.usage parsed as usage")
	}
}

func TestUsageFooterOnResponse(t *testing.T) {
	r, ok := describe(usageEvent("reason"), 60)
	if !ok {
		t.Fatal("response dropped")
	}
	if r.footer == "" {
		t.Fatal("response carries no usage footer")
	}
	lines := blockBody(r, 60)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "ctx 12%") {
		t.Fatalf("footer missing from body:\n%s", joined)
	}
	if !strings.Contains(joined, "in 100") || !strings.Contains(joined, "out 20") {
		t.Fatalf("footer missing in/out:\n%s", joined)
	}
}

func TestContextWorkerAndLine(t *testing.T) {
	c, _ := newTestChat(t)
	c.partners = []contract.Worker{{ID: "reason", Type: "reason", Label: "ReasonBot"}}
	c.providers = map[string]contract.ProviderList{
		"reason": {Current: contract.ProviderSelection{Provider: "openai", Model: "gpt-x"}},
	}
	c.events = []contract.Event{{Type: "worker.input"}, usageEvent("reason")}

	// all → the newest response's worker.
	if got := c.contextWorker(); got != "reason" {
		t.Fatalf("contextWorker = %q", got)
	}
	line := stripANSI(c.contextLine())
	t.Logf("context line: %q", line)
	// The worker is bracketed (not coloured — blue means clickable), and the
	// context shows a bare percentage.
	for _, want := range []string{"ctx", "[ReasonBot]", "12%", "model", "gpt-x"} {
		if !strings.Contains(line, want) {
			t.Fatalf("context line %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "120/1K") {
		t.Fatalf("context line should show a percentage, not the token split: %q", line)
	}

	// scoped to one worker → that worker, even with no events for it.
	c.filter = "reason"
	c.events = nil
	if got := c.contextWorker(); got != "reason" {
		t.Fatalf("scoped contextWorker = %q", got)
	}
}

func TestModelMenuFromCache(t *testing.T) {
	c, _ := newTestChat(t)
	c.partners = []contract.Worker{{ID: "reason", Type: "reason"}}
	c.providers = map[string]contract.ProviderList{
		"reason": {
			Providers: []contract.ProviderOption{
				{Name: "openai", Models: []string{"gpt-x", "gpt-y"}},
				{Name: "anthropic", Models: []string{"claude-z"}},
			},
			Current: contract.ProviderSelection{Provider: "openai", Model: "gpt-y"},
		},
	}
	c.modelWorker = "reason"
	c.menu = menuModel

	items := c.menuItems()
	if len(items) != 3 {
		t.Fatalf("model menu has %d rows, want 3: %+v", len(items), items)
	}
	// The current model is marked and is the one the list opens on.
	if got := c.menuIndex(menuModel); items[got].value != "openai\x1fgpt-y" {
		t.Fatalf("menu opened on %q, want the current model", items[got].value)
	}
	if !strings.Contains(items[c.menuIndex(menuModel)].note, "current") {
		t.Fatalf("current model not marked: %+v", items)
	}
}

// TestModelClickOpensPicker checks clicking the model on the context line opens
// the picker for the bound worker.
func TestModelClickOpensPicker(t *testing.T) {
	c, _ := newTestChat(t)
	c.partners = []contract.Worker{{ID: "reason", Type: "reason", Label: "ReasonBot"}}
	c.providers = map[string]contract.ProviderList{
		"reason": {Current: contract.ProviderSelection{Provider: "openai", Model: "gpt-x"}},
	}
	c.events = []contract.Event{usageEvent("reason")}
	c.render()

	if c.ctxRow < 0 || c.ctxModelX0 < 0 {
		t.Fatalf("context line not drawn: row=%d x0=%d", c.ctxRow, c.ctxModelX0)
	}
	// press coordinates are 1-based.
	c.handleKey(context.Background(), press{kind: pressClick, x: c.ctxModelX0 + 1, y: c.ctxRow + 1}, nil)
	if c.menu != menuModel {
		t.Fatalf("click on model opened menu %v, want menuModel", c.menu)
	}
	if c.modelWorker != "reason" {
		t.Fatalf("model picker bound to %q, want reason", c.modelWorker)
	}
}
