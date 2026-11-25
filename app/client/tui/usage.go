// Context-usage accounting read from a reason.response's payload.meta.usage —
// the same numbers the WebUI's ContextUsagePanel draws: the prompt/output token
// counts, the cache hit, and the model's context window. The worker attaches
// this per round (see the reason worker's contextbreakdown.go), so a response
// carries the occupancy it ran at.
package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// usageInfo is a reason.response's context accounting.
type usageInfo struct {
	input         int
	output        int
	total         int
	cacheRead     int
	cacheHitPct   float64
	contextWindow int
	hasCache      bool
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
	out.contextWindow = numInt(u["context_window"])
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
