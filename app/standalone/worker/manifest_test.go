package worker

import (
	"testing"

	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

func TestManifestFromSpawnCarriesMeta(t *testing.T) {
	cfg := coreworker.WorkerConfig{
		ID:          "oncall",
		Type:        "reason",
		Params:      map[string]any{"id": "oncall", "type": "reason"},
		Label:       "值班机器人",
		Tags:        []string{"work", "ops/backup"},
		Description: "值班专用",
	}
	decl := ManifestFromSpec(coreworker.WorkerSpec{WorkerConfig: cfg})
	if decl.ID != "oncall" || decl.Type != "reason" {
		t.Fatalf("decl base = %+v", decl)
	}
	if len(decl.Tags) != 2 || decl.Tags[0] != "work" || decl.Tags[1] != "ops/backup" {
		t.Fatalf("decl tags = %#v", decl.Tags)
	}
	if decl.Description != "值班专用" {
		t.Fatalf("decl description = %q", decl.Description)
	}
	if decl.Label != "值班机器人" {
		t.Fatalf("decl label = %q", decl.Label)
	}
	// Params stay verbatim (construction params only).
	if decl.Params["label"] != nil || decl.Params["tags"] != nil || decl.Params["description"] != nil {
		t.Fatalf("display metadata leaked into params: %+v", decl.Params)
	}
}
