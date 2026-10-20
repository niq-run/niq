package baseworker

import (
	"encoding/json"
	"strings"

	"github.com/niq-run/niq/core/itfs/event"
)

// RegisterReadme registers the worker.readme capability: a directed
// worker.readme call is answered with the worker's own guide.
//
// text runs per request so the guide can include live state (roster size,
// pending timers, configured servers). params is the declaration's construction
// params, read for two optional keys that let a declaration customize the guide
// without touching the worker's code:
//
//	readme         replaces the built-in guide
//	readme_append  is appended after it
//
// Both render {{key}} placeholders from params (top-level keys; list values
// join with ", "; an unknown key is left untouched). That is how a worker whose
// concrete duty only exists after configuration — an mcp worker with fixed
// servers and an allowed-tool list — states its actual role instead of the
// generic type guide.
func (w *BaseWorker) RegisterReadme(description string, text func() string, params map[string]any) {
	w.Register(Extension{
		Event:       event.TypeWorkerReadme,
		Response:    StringSchema("the worker's self-description (its readme text)"),
		Description: description,
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(evt event.Event) {
		tc := ParseToolCall(evt)
		if tc.CallID == "" {
			return
		}
		w.ReplyCompleted(tc.CallerID, tc.CallID, readmeText(text, params), tc.TraceID)
	})
}

// readmeText applies the declaration's readme customization over the worker's
// built-in guide (see RegisterReadme).
func readmeText(builtin func() string, params map[string]any) string {
	text := builtin()
	if override, ok := params["readme"].(string); ok && override != "" {
		text = override
	}
	if appendText, ok := params["readme_append"].(string); ok && appendText != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += appendText
	}
	return renderParams(text, params)
}

// renderParams replaces {{key}} with the params value. A placeholder with no
// matching key stays untouched, so ordinary prose and JSON braces are safe.
func renderParams(text string, params map[string]any) string {
	if len(params) == 0 || !strings.Contains(text, "{{") {
		return text
	}
	var b strings.Builder
	for {
		i := strings.Index(text, "{{")
		if i < 0 {
			break
		}
		rest := text[i+2:]
		j := strings.Index(rest, "}}")
		if j < 0 {
			break
		}
		b.WriteString(text[:i])
		key := strings.TrimSpace(rest[:j])
		if v, ok := params[key]; ok {
			b.WriteString(paramText(v))
		} else {
			b.WriteString(text[i : i+2+j+2])
		}
		text = rest[j+2:]
	}
	b.WriteString(text)
	return b.String()
}

// paramText renders one params value for a placeholder: strings verbatim,
// lists comma-joined, everything else as JSON.
func paramText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []string:
		return strings.Join(x, ", ")
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, paramText(e))
		}
		return strings.Join(parts, ", ")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
