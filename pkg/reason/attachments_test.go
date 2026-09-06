package reason

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/event"
)

func TestSplitAttachments(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("fakepng"))

	// Mixed order: text, image, text, file — segments keep their sequence.
	text := "看这张图\n<attachment type=\"image\" mime=\"image/png\" name=\"a.png\">" + b64 + "</attachment>\n中间的话\n<attachment type=\"file\" name=\"r.pdf\" path=\"/tmp/uploads/r.pdf\" size=\"42\"/>"
	parts := SplitAttachments(text)

	if len(parts) != 4 {
		t.Fatalf("parts = %d, want 4: %+v", len(parts), parts)
	}
	if parts[0].Kind != "text" || parts[0].Text != "看这张图\n" {
		t.Errorf("part0 = %+v", parts[0])
	}
	if parts[1].Kind != "image" || parts[1].MIME != "image/png" || parts[1].Name != "a.png" {
		t.Errorf("part1 = %+v", parts[1])
	}
	if got, _ := base64.StdEncoding.DecodeString(parts[1].Data); string(got) != "fakepng" {
		t.Errorf("image data decoded = %q", got)
	}
	if parts[2].Kind != "text" || parts[2].Text != "\n中间的话\n" {
		t.Errorf("part2 = %+v", parts[2])
	}
	if parts[3].Kind != "file" || parts[3].Path != "/tmp/uploads/r.pdf" || parts[3].Size != 42 {
		t.Errorf("part3 = %+v", parts[3])
	}
	if HasAttachments("plain input") {
		t.Error("plain input reported as having attachments")
	}
}

func TestSplitAttachmentsMalformedStaysText(t *testing.T) {
	// Invalid base64 and a file block without a path must stay in the text.
	text := "<attachment type=\"image\" mime=\"image/png\">!!!notbase64!!!</attachment>\n<attachment type=\"file\" name=\"x\"/>"
	parts := SplitAttachments(text)
	if len(parts) != 1 || parts[0].Kind != "text" {
		t.Fatalf("parts = %+v, want one text part", parts)
	}
	if !strings.Contains(parts[0].Text, "!!!notbase64!!!") || !strings.Contains(parts[0].Text, "<attachment type=\"file\"") {
		t.Errorf("raw blocks lost: %+v", parts[0])
	}
}

func TestConvertInputEventWithAttachments(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("fakejpeg"))
	evt := event.New(event.TypeWorkerInput, "webui-hiw", map[string]any{
		"text": "图如下\n<attachment type=\"image\" mime=\"image/jpeg\" name=\"a.jpg\">" + b64 + "</attachment>",
	})

	msgs := ConvertInputEvent(evt)
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("msgs = %+v", msgs)
	}
	content := msgs[0].Content
	if len(content) != 3 {
		t.Fatalf("content blocks = %d, want 3: %+v", len(content), content)
	}
	if content[0].Type != "text" || !strings.Contains(content[0].Text, "图如下") {
		t.Errorf("block0 = %+v", content[0])
	}
	if content[1].Type != "image" || content[1].MIMEType != "image/jpeg" || content[1].Data != b64 {
		t.Errorf("block1 = %+v", content[1])
	}
	if content[2].Type != "text" || !strings.Contains(content[2].Text, "[Event: worker.input from webui-hiw]") {
		t.Errorf("block2 = %+v", content[2])
	}
}

func TestConvertInputEventPlainFallsBackToDefault(t *testing.T) {
	evt := event.New(event.TypeWorkerInput, "webui-hiw", map[string]any{"text": "hello"})
	want := DefaultConverter(evt)
	got := ConvertInputEvent(evt)
	if len(got) != 1 || got[0].Content[0].Text != want[0].Content[0].Text {
		t.Errorf("plain input diverged from DefaultConverter:\n got %q\nwant %q", got[0].Content[0].Text, want[0].Content[0].Text)
	}
}
