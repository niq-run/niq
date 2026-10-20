package event

import "encoding/json"

// ReplyResult renders a request.completed payload's result as text: a string
// verbatim, any other JSON value encoded. Returns "" when the payload carries no
// result (e.g. a content-block reply, which uses the payload itself).
//
// The wire keeps types — a structured reply is real JSON, not a pre-encoded
// string — so every consumer needs this same rule: a reason worker before
// putting a tool result into its transcript, a script before handing it to the
// program, the WebUI before showing it, a test before asserting on it. Keeping
// one implementation is what keeps those views identical.
func ReplyResult(payload map[string]any) string {
	v, ok := payload["result"]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
