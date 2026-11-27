package baseworker

import (
	"errors"
	"testing"
)

func TestPatchConfigWithoutProvider(t *testing.T) {
	w := NewBaseWorker("w1", nil)
	err := w.PatchConfig(map[string]any{"remote_url": "http://x"})
	if !errors.Is(err, ErrConfigUnavailable) {
		t.Fatalf("err = %v, want ErrConfigUnavailable", err)
	}
}

func TestPatchConfigForwardsToInjectedProvider(t *testing.T) {
	w := NewBaseWorker("w1", nil)
	var got map[string]any
	w.SetConfigPatcher(func(patch map[string]any) error {
		got = patch
		return nil
	})

	if err := w.PatchConfig(map[string]any{"remote_url": "http://x", "note": nil}); err != nil {
		t.Fatalf("PatchConfig: %v", err)
	}
	if got["remote_url"] != "http://x" {
		t.Fatalf("patch = %+v", got)
	}
	if v, ok := got["note"]; !ok || v != nil {
		t.Fatalf("patch = %+v, want the nil entry preserved (null clears)", got)
	}
}

func TestPatchConfigPropagatesProviderError(t *testing.T) {
	w := NewBaseWorker("w1", nil)
	boom := errors.New("read-only")
	w.SetConfigPatcher(func(map[string]any) error { return boom })
	if err := w.PatchConfig(nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the provider's error", err)
	}
}
