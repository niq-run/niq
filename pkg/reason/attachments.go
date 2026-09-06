// HIW attachment envelope: the human input channel (webui-hiw) embeds
// attachments inside worker.input's text as <attachment> blocks. The envelope
// is a HIW-private convention — only the reason input converter interprets
// it; the bus and the event log see an ordinary text payload.
//
// Two spellings exist:
//
//	<attachment type="image" mime="image/png" name="shot.png">BASE64</attachment>
//	<attachment type="file" name="report.pdf" path="/…/uploads/report.pdf" size="4567"/>
package reason

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
)

// InputPart is one segment of an input text: a run of plain text, an image
// attachment (base64), or a file attachment (a path on disk).
type InputPart struct {
	Kind string // "text" | "image" | "file"
	Text string // kind=text: the raw text run

	// kind=image
	MIME string
	Data string // base64

	// kind=file
	Name string
	Path string
	Size int64
}

var (
	// attachmentRe matches both spellings: self-closing (`/>`) and paired
	// with a body (`>…</attachment>`). Group 1 carries the opening tag's
	// attributes, group 3 the body.
	attachmentRe = regexp.MustCompile(`(?s)<attachment\b([^>]*?)(/>|>(.*?)</attachment>)`)
	attrRe       = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_-]*)="([^"]*)"`)
)

// SplitAttachments splits an input text into its text runs and attachment
// blocks, in order. Malformed blocks (unknown type, non-image data on an
// image attachment) stay in the text as-is — nothing the human sent is
// silently dropped.
func SplitAttachments(text string) []InputPart {
	var parts []InputPart
	pos := 0
	for _, m := range attachmentRe.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[0], m[1]
		if start > pos {
			parts = appendText(parts, text[pos:start])
		}
		rawAttrs := text[m[2]:m[3]]
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(rawAttrs, -1) {
			attrs[strings.ToLower(a[1])] = a[2]
		}
		body := ""
		if m[6] != -1 {
			body = text[m[6]:m[7]]
		}

		switch attrs["type"] {
		case "image":
			if part, ok := imagePart(attrs, body); ok {
				parts = append(parts, part)
				pos = end
				continue
			}
		case "file":
			size := int64(0)
			fmt.Sscanf(attrs["size"], "%d", &size)
			if attrs["path"] != "" {
				parts = append(parts, InputPart{Kind: "file", Name: attrs["name"], Path: attrs["path"], Size: size})
				pos = end
				continue
			}
		}
		// Unrecognized or malformed: keep the raw block as text.
		parts = appendText(parts, text[start:end])
		pos = end
	}
	if pos < len(text) {
		parts = appendText(parts, text[pos:])
	}
	return parts
}

// imagePart builds an image part from attrs + base64 body, rejecting empty
// data and data that is not valid base64 (fail-open to raw text).
func imagePart(attrs map[string]string, body string) (InputPart, bool) {
	b64 := strings.Join(strings.Fields(body), "")
	if b64 == "" {
		return InputPart{}, false
	}
	if _, err := base64.StdEncoding.DecodeString(b64); err != nil {
		return InputPart{}, false
	}
	mime := attrs["mime"]
	if !strings.HasPrefix(mime, "image/") {
		mime = "image/png"
	}
	return InputPart{Kind: "image", MIME: mime, Data: b64, Name: attrs["name"]}, true
}

// appendText merges a run into the last part when it is also text, so
// adjacent text stays one block.
func appendText(parts []InputPart, run string) []InputPart {
	if run == "" {
		return parts
	}
	if n := len(parts); n > 0 && parts[n-1].Kind == "text" {
		parts[n-1].Text += run
		return parts
	}
	return append(parts, InputPart{Kind: "text", Text: run})
}

// HasAttachments reports whether an input text carries any attachment block.
func HasAttachments(text string) bool {
	return attachmentRe.MatchString(text)
}

// ConvertInputEvent turns a worker.input event into user messages. Text-only
// input keeps the DefaultConverter shape; input carrying the HIW attachment
// envelope is split into content blocks — image attachments become
// ContentImage, file attachments become text notes pointing at their path
// (workspace tools read them from disk).
func ConvertInputEvent(evt event.Event) []llm.Message {
	text, _ := evt.Payload["text"].(string)
	if text == "" || !HasAttachments(text) {
		return DefaultConverter(evt)
	}

	var content []llm.ContentBlock
	for _, p := range SplitAttachments(text) {
		switch p.Kind {
		case "image":
			content = append(content, llm.ContentBlock{Type: llm.ContentImage, Data: p.Data, MIMEType: p.MIME})
		case "file":
			content = append(content, llm.ContentBlock{Type: llm.ContentText,
				Text: fmt.Sprintf("[user attached file: %s (%d bytes)]", p.Path, p.Size)})
		default:
			content = append(content, llm.ContentBlock{Type: llm.ContentText, Text: p.Text})
		}
	}
	// Event metadata footer, same convention as DefaultConverter.
	content = append(content, llm.ContentBlock{Type: llm.ContentText,
		Text: fmt.Sprintf("[Event: %s from %s]", evt.Type, evt.WorkerId)})
	return []llm.Message{{Role: llm.RoleUser, Content: content}}
}
