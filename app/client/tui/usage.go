// Context-usage accounting read from a reason.response's payload.meta.usage —
// the same numbers the WebUI's ContextUsagePanel draws: the prompt/output token
// counts, the cache hit, and the model's context window. The worker attaches
// this per round (see the reason worker's contextbreakdown.go), so a response
// carries the occupancy it ran at. The detail panel behind the dock's percentage
// (the popup) lives in the same file.
package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/niq-run/niq/app/client/tui/ui"
)

// usageInfo is a reason.response's context accounting.
type usageInfo struct {
	input         int
	output        int
	total         int
	cacheRead     int
	cacheCreation int
	cacheHitPct   float64
	contextWindow int
	hasCache      bool
	breakdown     []usageSection
}

// usageSection is one keyed slice of the round's prompt (system, tools.builtin,
// messages.user, …), as the reason worker reports it on meta.usage.breakdown.
type usageSection struct {
	key    string
	tokens int
}

// fraction is total tokens as a percentage of the context window (0 when the
// window is unknown).
func (u usageInfo) fraction() float64 {
	if u.contextWindow <= 0 {
		return 0
	}
	return float64(u.total) / float64(u.contextWindow) * 100
}

// parseUsage reads payload.meta.usage. ok is false when the response carries no
// usage (an older event, or a provider that reported none).
func parseUsage(payload map[string]any) (usageInfo, bool) {
	meta, _ := payload["meta"].(map[string]any)
	if meta == nil {
		return usageInfo{}, false
	}
	u, _ := meta["usage"].(map[string]any)
	if u == nil {
		return usageInfo{}, false
	}
	var out usageInfo
	out.input = numInt(u["input_tokens"])
	out.output = numInt(u["output_tokens"])
	out.total = numInt(u["total_tokens"])
	if out.total == 0 {
		out.total = out.input + out.output
	}
	if v, ok := u["cache_read_tokens"]; ok {
		out.cacheRead = numInt(v)
		out.hasCache = true
	}
	out.cacheHitPct = numFloat(u["cache_hit_percent"])
	out.cacheCreation = numInt(u["cache_creation_tokens"])
	out.contextWindow = numInt(u["context_window"])
	if raw, ok := u["breakdown"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			key, _ := m["key"].(string)
			tokens := numInt(m["tokens"])
			if key == "" || tokens <= 0 {
				continue
			}
			out.breakdown = append(out.breakdown, usageSection{key: key, tokens: tokens})
		}
	}
	return out, true
}

// numInt normalizes a JSON number to an int. Events decoded from the SSE stream
// carry float64; events built in-process may carry int — the same payload parses
// either way.
func numInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// numFloat normalizes a JSON number to a float.
func numFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

// compactTokens abbreviates a token count: 128000 → "128K", 1200000 → "1.2M",
// 999 → "999".
func compactTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return trimDecimal(float64(n)/1_000_000) + "M"
	case n >= 1000:
		return fmt.Sprintf("%dK", (n+500)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// trimDecimal formats with one decimal only when it adds information ("1.2" vs
// "2").
func trimDecimal(f float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
}

// formatPercent renders a 0..100 occupancy without collapsing a small value to
// "0": below 10 it keeps one decimal, otherwise it rounds. Exact zero is "0".
func formatPercent(p float64) string {
	if p <= 0 {
		return "0"
	}
	if p < 10 {
		return trimDecimal(p)
	}
	return fmt.Sprintf("%d", int(p+0.5))
}

// occupancy is the compact "percent · used/window" a glance needs, e.g.
// "12% · 36K/128K"; just the token count when the window is unknown.
func (u usageInfo) occupancy() string {
	if u.contextWindow > 0 {
		return fmt.Sprintf("%s%% · %s/%s", formatPercent(u.fraction()), compactTokens(u.total), compactTokens(u.contextWindow))
	}
	return compactTokens(u.total)
}

// percent is the occupancy as a bare percentage ("12%"), for the dock's compact
// context label; the token count stands in when the window is unknown.
func (u usageInfo) percent() string {
	if u.contextWindow > 0 {
		return formatPercent(u.fraction()) + "%"
	}
	return compactTokens(u.total)
}

// share is a slice's tokens as a percentage of the window, or the raw count when
// the window is unknown.
func (u usageInfo) share(tokens int) string {
	if u.contextWindow > 0 && tokens > 0 {
		return formatPercent(float64(tokens)/float64(u.contextWindow)*100) + "%"
	}
	return compactTokens(tokens)
}

// cacheFooter is the detail panel's closing line: cache hit and the window size.
func (u usageInfo) cacheFooter() string {
	var parts []string
	if u.hasCache {
		parts = append(parts, "cache "+formatPercent(u.cacheHitPct)+"%")
	}
	if u.cacheRead > 0 {
		parts = append(parts, "read "+compactTokens(u.cacheRead))
	}
	if u.cacheCreation > 0 {
		parts = append(parts, "write "+compactTokens(u.cacheCreation))
	}
	if u.contextWindow > 0 {
		parts = append(parts, "window "+compactTokens(u.contextWindow))
	}
	return strings.Join(parts, " · ")
}

// footer is the dim label a response carries at its end: occupancy plus the
// round's input/output split and cache hit.
func (u usageInfo) footer() string {
	parts := []string{"ctx " + u.occupancy()}
	if u.input > 0 {
		parts = append(parts, "in "+compactTokens(u.input))
	}
	if u.output > 0 {
		parts = append(parts, "out "+compactTokens(u.output))
	}
	if u.hasCache && u.cacheHitPct > 0 {
		parts = append(parts, "cache "+formatPercent(u.cacheHitPct)+"%")
	}
	return strings.Join(parts, " · ")
}

// ── Detail panel ──

// usageGroupDefs is the WebUI's grouping of the worker's fine-grained breakdown
// keys (per tool source, per message kind) into the rows worth showing, in order.
var usageGroupDefs = []struct {
	label string
	keys  []string
}{
	{"system", []string{"system"}},
	{"program", []string{"program.instructions", "program.playbooks"}},
	{"tools", []string{"tools.builtin", "tools.program", "tools.mcp", "tools.workers", "tools.other"}},
	{"messages", []string{"messages.user", "messages.assistant"}},
	{"tool calls", []string{"messages.tool_calls", "messages.tool_results"}},
	{"thinking", []string{"messages.thinking"}},
	{"output", []string{"output"}},
}

// usageGroup is one legend row: a label and the tokens attributed to it.
type usageGroup struct {
	label  string
	tokens int
}

// groups aggregates the fine-grained breakdown into the display rows, dropping
// empty ones.
func (u usageInfo) groups() []usageGroup {
	sum := make(map[string]int, len(u.breakdown))
	for _, s := range u.breakdown {
		sum[s.key] += s.tokens
	}
	var out []usageGroup
	for _, d := range usageGroupDefs {
		t := 0
		for _, k := range d.keys {
			t += sum[k]
		}
		if t > 0 {
			out = append(out, usageGroup{label: d.label, tokens: t})
		}
	}
	return out
}

// usageCard lays the detail out as a modal card.
func (c *chat) usageCard() ([]string, int) {
	worker := c.usageWorker
	u, ok := c.usageFor(worker)
	title := "context"
	var body []string
	if !ok {
		body = append(body, dim("no usage reported for this worker yet"))
		return ui.Dialog{Title: title, Footer: "esc or click outside", Body: body}.Lines(c.width, c.height)
	}

	title = "context · " + c.workerDisplay(worker)
	headline := brandBold(u.percent())
	if u.contextWindow > 0 {
		headline += dim(fmt.Sprintf("  %s / %s", compactTokens(u.total), compactTokens(u.contextWindow)))
	} else {
		headline += dim("  " + compactTokens(u.total) + " tokens")
	}
	body = append(body, headline, "")

	if groups := u.groups(); len(groups) > 0 {
		for _, g := range groups {
			label := fmt.Sprintf("%-12s", g.label)
			body = append(body, label+fmt.Sprintf("%7s", compactTokens(g.tokens))+"  "+dim(fmt.Sprintf("%6s", u.share(g.tokens))))
		}
	} else {
		body = append(body, dim("no breakdown reported"))
	}

	if footer := u.cacheFooter(); footer != "" {
		body = append(body, "", dim(footer))
	}
	return ui.Dialog{Title: title, Footer: "esc or click outside", Body: body}.Lines(c.width, c.height)
}

// paintUsage overlays the detail card on the frame and records its block, so a
// click outside it closes it.
func (c *chat) paintUsage(rows []string) {
	card, boxW := c.usageCard()
	c.cardRect = ui.PaintCard(rows, card, boxW, c.width)
}

// openUsage raises the detail card for the dock's bound worker.
func (c *chat) openUsage() {
	w := c.contextWorker()
	if w == "" {
		return
	}
	c.usageWorker = w
	c.usagePanel = true
}
