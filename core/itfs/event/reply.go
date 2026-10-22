package event

import "encoding/json"

// The reply conventions of a request.completed payload.
//
// The bus only requires the payload to be a JSON object; the VALUE a caller is
// after is either the payload itself (an object reply) or, when the reply is not
// an object (text, a number, an array), its "result" entry. "content" is the
// second reserved key: multi-modal blocks a reason worker decodes into llm
// content blocks.
//
// ReplyValue / ReplyText are the one place that rule lives, so every consumer —
// the reason worker before its transcript, a script's call, the WebUI's tool
// views, tests — reads the same value out of the same payload.

// ReplyValue returns the reply carried by a request.completed payload: the
// payload itself for an object reply, otherwise its "result" value.
func ReplyValue(payload map[string]any) any {
	if v, ok := payload["result"]; ok {
		return v
	}
	return payload
}

// ReplyText renders that value as text: a string verbatim, anything else JSON.
// This is what a reason worker puts into its transcript and what the WebUI
// shows for a tool reply.
func ReplyText(payload map[string]any) string {
	v := ReplyValue(payload)
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	// An empty payload is "nothing to say" rather than the JSON text "{}".
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
