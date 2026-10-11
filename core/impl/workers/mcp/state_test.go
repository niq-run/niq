package mcp

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestFileServerStoreRoundTrip(t *testing.T) {
	store := NewFileServerStore(filepath.Join(t.TempDir(), "servers.json"))
	if got, err := store.Load(); err != nil || got != nil {
		t.Fatalf("fresh load = %v, %v; want nil,nil", got, err)
	}
	want := []ServerConfig{
		{Name: "a", URL: "https://a", Headers: map[string]string{"k": "v"}},
		{Name: "b", URL: "https://b"},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roundtrip = %+v, want %+v", got, want)
	}
}