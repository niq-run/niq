// Payload reading: turning the open event payload map into the text the
// transcript shows — content arrays, string fields, and a one-line JSON view.
package text

import (
	"encoding/json"
	"regexp"
	"strings"
)

// reminderRe matches a <system-reminder> block the WebUI inserts into a user
// turn (interrupt/board notices the human should not see as their own words).
var reminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// StripSystemReminder removes those blocks from a user message before it is
// shown, leaving only what the human actually typed.
func StripSystemReminder(s string) string {
	if !strings.Contains(s, "<system-reminder>") {
		return s
	}
	return strings.TrimSpace(reminderRe.ReplaceAllString(s, ""))
}

// Sanitize makes text safe to draw. A control byte would be interpreted by
// the terminal — an ESC sequence moves the cursor or changes colours, which
// punches holes in whatever surface the line is painted on — so every one is
// replaced by a visible dot. Newlines are structural and stay; a tab is expanded,
// since the terminal's tab stops would desynchronise our own cell counting.
func Sanitize(s string) string {
	clean := true
	for _, r := range s {
		if IsControl(r) || r == '\t' {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case IsControl(r):
			b.WriteRune('·')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ContentText reads the text out of an event's payload: a structured "content"
// array (llm-like blocks, or the string array reason.response carries) when
// present, otherwise the legacy "text" field.
func ContentText(content any, legacyText any) string {
	if s := PayloadText(content); s != "" {
		return Sanitize(s)
	}
	if s, ok := legacyText.(string); ok {
		return Sanitize(s)
	}
	return ""
}

// PayloadText flattens a "content" array into display text. Text blocks (and
// bare strings) contribute their text; media blocks contribute a short marker so
// the reader knows something is attached without dumping base64 into a terminal.
func PayloadText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, item := range c {
			switch b := item.(type) {
			case string:
				if b != "" {
					parts = append(parts, b)
				}
			case map[string]any:
				kind, _ := b["type"].(string)
				text, _ := b["text"].(string)
				switch kind {
				case "", "text":
					if text != "" {
						parts = append(parts, text)
					}
				case "image":
					parts = append(parts, "[image]")
				case "file":
					name, _ := b["name"].(string)
					if name == "" {
						name, _ = b["path"].(string)
					}
					parts = append(parts, "[file "+name+"]")
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// StringField reads a string field from a payload, or "" when absent.
func StringField(p map[string]any, key string) string {
	if s, ok := p[key].(string); ok {
		return Sanitize(s)
	}
	return ""
}

// JSONOneLine is a payload as one line of JSON, for the event rows. Empty
// payloads render as no detail at all. HTML escaping is off so a command reads
// as written (`&&`, `<`, `>`) instead of as \u0026-style escapes.
func JSONOneLine(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSpace(b.String()), "\n", " ")
}
