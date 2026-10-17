package workspace

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"testing"
)

// fakePNG renders a MaxEdge+2 square of solid color and encodes it as PNG bytes.
func fakePNG(t *testing.T, edge int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, edge, edge))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestReadImagePayloadCapsLongEdge(t *testing.T) {
	raw := fakePNG(t, readImageLongEdge+100) // 1668x1668 -> must be scaled to 1568
	payload, err := readImagePayload(raw)
	if err != nil {
		t.Fatalf("readImagePayload: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload = %#v, want map[string]any", payload)
	}
	arr, ok := m["content"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("payload content = %#v", m["content"])
	}
	block, ok := arr[0].(map[string]any)
	if !ok {
		t.Fatalf("block = %#v", arr[0])
	}
	if block["type"] != "image" {
		t.Errorf("type = %v, want image", block["type"])
	}
	data, _ := base64.StdEncoding.DecodeString(block["data"].(string))
	got, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding output: %v (data len=%d)", err, len(data))
	}
	if got.Bounds().Dx() > readImageLongEdge || got.Bounds().Dy() > readImageLongEdge {
		t.Errorf("long edge = %d/%d, want <= %d", got.Bounds().Dx(), got.Bounds().Dy(), readImageLongEdge)
	}
}

func TestReadImagePayloadKeepsSmallImage(t *testing.T) {
	raw := fakePNG(t, 64)
	payload, err := readImagePayload(raw)
	if err != nil {
		t.Fatalf("readImagePayload: %v", err)
	}
	m := payload.(map[string]any)
	arr := m["content"].([]any)
	block := arr[0].(map[string]any)
	data, _ := base64.StdEncoding.DecodeString(block["data"].(string))
	got, _ := png.Decode(bytes.NewReader(data))
	if got.Bounds().Dx() != 64 || got.Bounds().Dy() != 64 {
		t.Errorf("dimensions = %d/%d, want 64/64", got.Bounds().Dx(), got.Bounds().Dy())
	}
	if mime, _ := block["mime"].(string); mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
}

func TestReadImagePayloadRejectsNonImage(t *testing.T) {
	if _, err := readImagePayload([]byte("this is definitely not an image")); err == nil {
		t.Error("expected an error for a non-image payload")
	}
}
