// Decoding an external event payload's structured "content" array into llm
// content blocks.
//
// External events may carry their content as a structured "content" array in
// their payload — the current form of both a user input (worker.input) and a
// tool result (request.completed / failed / rejected). PayloadToLlmContent is
// the shared "external event → llm content blocks" conversion: it decodes that
// array into llm.ContentBlock values (text / image / file-note) regardless of
// which kind of event carried it.
//
// A plain-text event that has no "content" array is not touched here; callers
// (the reason input converter, the tool-result outcome extractor) fall back to
// their own text handling.
package transcript

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/niq-run/niq/core/itfs/llm"
)

// eventBlock is one element of a payload's structured "content" array. The
// wire shape mirrors llm.ContentBlock for text and image, plus a file note
// (a path on disk that workspace tools read; converted to a text hint for the
// model, since the model cannot read the file itself).
type eventBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// image
	Name string `json:"name,omitempty"`
	MIME string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"` // base64

	// file
	Path string `json:"path,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// PayloadToLlmContent decodes a payload's structured "content" array (when
// present) into llm content blocks. ok is false when the payload has no
// content array or it does not decode — the caller then falls back to its own
// plain-text handling.
//
// Each block maps:
//
//	text  -> ContentText (raw, incl. any <system-reminder> prefix)
//	image -> ContentImage (base64, defaulting mime to image/png)
//	file  -> ContentText note pointing at the on-disk path
//
// A malformed block is not dropped silently; it becomes a text note so the
// model still sees the sender tried to send something.
func PayloadToLlmContent(payload map[string]any) ([]llm.ContentBlock, bool) {
	raw, ok := payload["content"]
	if !ok {
		return nil, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var arr []eventBlock
	if err := json.Unmarshal(b, &arr); err != nil || len(arr) == 0 {
		return nil, false
	}
	var blocks []llm.ContentBlock
	for _, c := range arr {
		switch c.Type {
		case "text":
			blocks = append(blocks, llm.ContentBlock{Type: llm.ContentText, Text: c.Text})
		case "image":
			b64 := strings.Join(strings.Fields(c.Data), "")
			if b64 == "" || !validBase64(b64) {
				blocks = append(blocks, llm.ContentBlock{Type: llm.ContentText, Text: "[invalid image content]"})
				continue
			}
			mime := c.MIME
			if !strings.HasPrefix(mime, "image/") {
				mime = "image/png"
			}
			blocks = append(blocks, llm.ContentBlock{Type: llm.ContentImage, Data: b64, MIMEType: mime})
		case "file":
			blocks = append(blocks, llm.ContentBlock{Type: llm.ContentText,
				Text: fmt.Sprintf("[referenced file: %s (%d bytes)]", c.Path, c.Size)})
		default:
			// Unknown type: keep any text it carries rather than dropping the block.
			if c.Text != "" {
				blocks = append(blocks, llm.ContentBlock{Type: llm.ContentText, Text: c.Text})
			}
		}
	}
	return blocks, true
}

// validBase64 reports whether s decodes as standard base64.
func validBase64(s string) bool {
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
}
