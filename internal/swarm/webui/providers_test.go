package webui

import (
	"encoding/json"
	"testing"
)

// nativeProvider mirrors the shape a reason worker's payload actually carries:
// a managed worker runs in this process, so worker.status never crosses a
// serialization boundary and the value stays the worker's own Go slice of
// structs rather than becoming a []any of maps.
type nativeProvider struct {
	Name    string   `json:"name"`
	Default string   `json:"default"`
	Models  []string `json:"models"`
}

// TestProvidersFromPayloadNativeType is the regression guard for the section
// reporting "no switchable providers" even though the worker had answered: the
// payload was read with a []any assertion, which a Go-native slice never
// satisfies.
func TestProvidersFromPayloadNativeType(t *testing.T) {
	got := providersFromPayload([]nativeProvider{
		{Name: "siliconflow", Default: "THUDM/GLM-Z1-9B-0414", Models: []string{"THUDM/GLM-Z1-9B-0414", "openrouter/free"}},
		{Name: "deepseek", Default: "deepseek-v4-flash", Models: []string{"deepseek-v4-flash"}},
	})
	if len(got) != 2 {
		t.Fatalf("got %d providers, want 2", len(got))
	}
	if got[0].Name != "siliconflow" || got[0].Default != "THUDM/GLM-Z1-9B-0414" {
		t.Fatalf("first provider = %+v, want siliconflow/THUDM/GLM-Z1-9B-0414", got[0])
	}
	if len(got[0].Models) != 2 {
		t.Fatalf("first provider models = %v, want 2 entries", got[0].Models)
	}
}

// TestProvidersFromPayloadDecodedJSON covers the other shape: a payload that
// did come from JSON (an external worker over the HTTP transport), where the
// value is a []any of maps.
func TestProvidersFromPayloadDecodedJSON(t *testing.T) {
	var decoded any
	if err := json.Unmarshal([]byte(`[{"name":"openrouter","default":"openrouter/free","models":["openrouter/free"]}]`), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := providersFromPayload(decoded)
	if len(got) != 1 {
		t.Fatalf("got %d providers, want 1", len(got))
	}
	if got[0].Name != "openrouter" || len(got[0].Models) != 1 || got[0].Models[0] != "openrouter/free" {
		t.Fatalf("provider = %+v, want openrouter with one model", got[0])
	}
}

// TestProvidersFromPayloadEmpty verifies a missing or unusable value degrades to
// an empty list instead of a nil one (the UI only renders the empty-state text
// for a zero-length list).
func TestProvidersFromPayloadEmpty(t *testing.T) {
	for name, v := range map[string]any{
		"nil":       nil,
		"absent":    nil,
		"wrongType": "not a list",
	} {
		got := providersFromPayload(v)
		if got == nil {
			t.Fatalf("%s: got nil, want empty non-nil slice", name)
		}
		if len(got) != 0 {
			t.Fatalf("%s: got %d providers, want 0", name, len(got))
		}
	}
}
