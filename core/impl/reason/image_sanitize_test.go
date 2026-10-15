package reason

import (
	"testing"

	"github.com/niq-run/niq/core/itfs/llm"
)

// m with a single content block.
func msgImage(role llm.Role, text string) llm.Message {
	return llm.Message{Role: role, Content: imageBlock()}
}

func imageBlock() []llm.ContentBlock {
	return []llm.ContentBlock{{Type: llm.ContentImage, Data: "aGVsbG8=", MIMEType: "image/png"}}
}

func TestSanitizeImagesReplacesImageWithHint(t *testing.T) {
	in := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.ContentText, Text: "看图"},
			{Type: llm.ContentImage, Data: "aGVsbG8=", MIMEType: "image/png"},
		}},
		msgImage(llm.RoleToolResult, ""),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}},
	}
	out := sanitizeImages(in)
	if len(out) != 3 {
		t.Fatalf("out len = %d, want 3", len(out))
	}
	// User message: image becomes a hint, text kept.
	user := out[0]
	if user.Content[0].Type != llm.ContentText || user.Content[0].Text != "看图" {
		t.Errorf("user[0] = %+v", user.Content[0])
	}
	if user.Content[1].Type != llm.ContentText || user.Content[1].Text != "[image omitted: the active model does not support image inputs]" {
		t.Errorf("user[1] = %+v", user.Content[1])
	}
	// Tool-result message: image replaced.
	if tr := out[1]; tr.Content[0].Type != llm.ContentText {
		t.Errorf("tool-result[0] = %+v", tr.Content[0])
	}
	// Assistant text untouched.
	if as := out[2]; as.Content[0].Type != llm.ContentText || as.Content[0].Text != "ok" {
		t.Errorf("assistant = %+v", as.Content[0])
	}
	// The input slice must be left untouched.
	if in[0].Content[1].Type != llm.ContentImage {
		t.Error("sanitizeImages mutated its input")
	}
}

func TestSanitizeImagesPassesThroughNoImage(t *testing.T) {
	out := sanitizeImages([]llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}}},
	})
	if len(out) != 1 || out[0].Content[0].Type != llm.ContentText || len(out[0].Content) != 1 {
		t.Fatalf("out = %+v", out)
	}
}

func TestIsImageInputType(t *testing.T) {
	for _, ok := range []string{"image", "image_input", "image_url", "vision", "multimodal"} {
		if !isImageInputType(ok) {
			t.Errorf("isImageInputType(%q) = false, want true", ok)
		}
	}
	for _, no := range []string{"text", "audio", "video"} {
		if isImageInputType(no) {
			t.Errorf("isImageInputType(%q) = true, want false", no)
		}
	}
}

func TestModelSupportsImages(t *testing.T) {
	cases := []struct {
		name   string
		infos  []ProviderInfo
		model  string
		expect bool
	}{
		{
			name: "unknown modalities assume supports",
			infos: []ProviderInfo{{Name: "a", Default: "m1", Models: []string{"m1"},
				ModelDetails: []llm.ModelInfo{{ID: "m1", Name: "m1"}}}},
			model:  "m1",
			expect: true,
		},
		{
			name: "explicit image modality supports",
			infos: []ProviderInfo{{Name: "a", Default: "m1", Models: []string{"m1"},
				ModelDetails: []llm.ModelInfo{{ID: "m1", Name: "m1", InputTypes: []string{"text", "image"}}}}},
			model:  "m1",
			expect: true,
		},
		{
			name: "explicit text-only does not support",
			infos: []ProviderInfo{{Name: "a", Default: "m1", Models: []string{"m1"},
				ModelDetails: []llm.ModelInfo{{ID: "m1", Name: "m1", InputTypes: []string{"text"}}}}},
			model:  "m1",
			expect: false,
		},
		{
			name:   "no provider sources supports",
			infos:  nil,
			model:  "",
			expect: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Declare the sources as the interface type so the "no provider
			// sources" case is a genuine nil interface, not a typed-nil pointer
			// (which the worker's != nil checks would treat as present).
			var sources ProviderSources
			if tc.infos != nil {
				sources = &fakeSources{infos: tc.infos, provs: map[string]llm.LLMProvider{}}
			}
			w := NewBaseReasonWorker(Config{ID: "r1", ProviderSources: sources, Bus: newTestChannel()})
			if w.providerName == "" {
				w.providerName = "a"
			}
			if tc.model != "" {
				w.providerModel = tc.model
			}
			if got := w.modelSupportsImages(); got != tc.expect {
				t.Errorf("modelSupportsImages() = %v, want %v", got, tc.expect)
			}
		})
	}
}
