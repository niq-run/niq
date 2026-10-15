package transcript

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/llm"
)

// Tests for PayloadToLlmContent — the shared decoder that turns a payload's
// structured "content" array into llm content blocks.

func TestPayloadToLlmContentTextImageFile(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("fakejpeg"))
	blocks, ok := PayloadToLlmContent(map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "图如下"},
			map[string]any{"type": "image", "name": "a.jpg", "mime": "image/jpeg", "data": b64},
			map[string]any{"type": "file", "name": "r.pdf", "path": "/tmp/uploads/r.pdf", "size": float64(42)},
		},
	})
	if !ok {
		t.Fatal("PayloadToLlmContent returned ok=false")
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want 3: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != llm.ContentText || blocks[0].Text != "图如下" {
		t.Errorf("block0 = %+v", blocks[0])
	}
	if blocks[1].Type != llm.ContentImage || blocks[1].MIMEType != "image/jpeg" || blocks[1].Data != b64 {
		t.Errorf("block1 = %+v", blocks[1])
	}
	if blocks[2].Type != llm.ContentText || !strings.Contains(blocks[2].Text, "/tmp/uploads/r.pdf") || !strings.Contains(blocks[2].Text, "42") {
		t.Errorf("block2 = %+v", blocks[2])
	}
}

func TestPayloadToLlmContentMimeDefaults(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("x"))
	blocks, ok := PayloadToLlmContent(map[string]any{
		"content": []any{
			map[string]any{"type": "image", "data": b64}, // no mime -> image/png
		},
	})
	if !ok || len(blocks) != 1 {
		t.Fatalf("blocks = %+v, ok=%v", blocks, ok)
	}
	if blocks[0].MIMEType != "image/png" {
		t.Errorf("mime = %q, want image/png", blocks[0].MIMEType)
	}
}

func TestPayloadToLlmContentInvalidImageBecomesTextNote(t *testing.T) {
	// A malformed base64 image must not be dropped silently: it becomes a text
	// note rather than a ContentImage.
	blocks, ok := PayloadToLlmContent(map[string]any{
		"content": []any{
			map[string]any{"type": "image", "mime": "image/png", "data": "!!!notbase64!!!"},
		},
	})
	if !ok || len(blocks) != 1 {
		t.Fatalf("blocks = %+v, ok=%v", blocks, ok)
	}
	if blocks[0].Type != llm.ContentText || !strings.Contains(blocks[0].Text, "invalid image") {
		t.Errorf("block0 = %+v", blocks[0])
	}
}

func TestPayloadToLlmContentAbsentIsNotOk(t *testing.T) {
	if _, ok := PayloadToLlmContent(map[string]any{"text": "hello"}); ok {
		t.Error("payload without a content array reported ok")
	}
	if _, ok := PayloadToLlmContent(nil); ok {
		t.Error("nil payload reported ok")
	}
}
