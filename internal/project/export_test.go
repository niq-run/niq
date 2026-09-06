package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exportTemplate is a template covering every exportable field: a managed
// reason worker with provider config, subscriptions, publish and mounts, plus
// an unmanaged worker carrying a launch spec.
func exportTemplate() *TemplateConfig {
	no := false
	return &TemplateConfig{Workers: []WorkerConfig{
		{
			Type: "reason", ID: "niq",
			Instruction: "do the thing",
			Provider:    "volcan-ark", APIKey: "sk-secret", BaseURL: "https://api.example.com",
			Model:         "deepseek-v4-flash",
			Subscriptions: []SubscriptionSpec{{Type: "timer.reminder", Source: "timer"}},
			Publish:       []PublishSpec{{Type: "reason.response"}},
			Mounts:        []string{"/tmp/demo"},
			Approver:      "webui-hiw",
		},
		{
			Type: "reason", ID: "retired",
			Instruction: "this one gets archived",
		},
		{
			Type: "http", ID: "ext-1", Managed: &no,
			Credential: "tok-secret",
			Command:    []string{"./worker-ts", "run"},
			Env:        map[string]string{"PORT": "8080"},
			Cwd:        "/tmp/ext",
			Publish:    []PublishSpec{{Type: "pr.ready", Target: "niq"}},
		},
	}}
}

// TestExportProjectTemplateRoundTrip creates a project from a template, marks
// one worker archived, exports it back, and compares the re-parsed result:
// expressible fields survive, secrets do not, the archived worker is gone.
func TestExportProjectTemplateRoundTrip(t *testing.T) {
	setupProjectsRoot(t)
	src := exportTemplate()
	if _, err := CreateProject("alpha", src); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Archive the second worker.
	p, err := LoadProject("alpha")
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	p.Archived = map[string]bool{"retired": true}
	if err := SaveProject(p); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	out, err := ExportProjectTemplate("alpha")
	if err != nil {
		t.Fatalf("ExportProjectTemplate: %v", err)
	}
	if len(out.Workers) != 2 {
		t.Fatalf("exported %d workers, want 2 (archived skipped): %+v", len(out.Workers), out.Workers)
	}

	// Managed worker: expressible fields round-trip, api_key does not.
	var niq WorkerConfig
	for _, w := range out.Workers {
		if w.ID == "niq" {
			niq = w
		}
	}
	if niq.Type != "reason" {
		t.Fatalf("niq type = %q", niq.Type)
	}
	want := src.Workers[0]
	if niq.Instruction != want.Instruction || niq.Provider != want.Provider ||
		niq.BaseURL != want.BaseURL || niq.Model != want.Model ||
		niq.Approver != want.Approver {
		t.Fatalf("niq fields drifted:\n got %+v\nwant %+v", niq, want)
	}
	if len(niq.Mounts) != 1 || niq.Mounts[0] != want.Mounts[0] {
		t.Fatalf("niq mounts = %v, want %v", niq.Mounts, want.Mounts)
	}
	if len(niq.Subscriptions) != 1 || niq.Subscriptions[0] != want.Subscriptions[0] {
		t.Fatalf("niq subscriptions = %+v, want %+v", niq.Subscriptions, want.Subscriptions)
	}
	if len(niq.Publish) != 1 || niq.Publish[0] != want.Publish[0] {
		t.Fatalf("niq publish = %+v, want %+v", niq.Publish, want.Publish)
	}
	if niq.APIKey != "" {
		t.Fatal("api_key must not enter the exported template")
	}
	if niq.Managed != nil && !*niq.Managed {
		t.Fatal("managed worker must stay managed")
	}

	// Unmanaged worker: launch spec survives, credential does not.
	var ext WorkerConfig
	for _, w := range out.Workers {
		if w.ID == "ext-1" {
			ext = w
		}
	}
	if ext.ID == "" {
		t.Fatal("unmanaged worker ext-1 missing from export")
	}
	if ext.Command == nil || len(ext.Command) != 2 || ext.Env["PORT"] != "8080" || ext.Cwd != "/tmp/ext" {
		t.Fatalf("unmanaged launch spec drifted: %+v", ext)
	}
	if len(ext.Publish) != 1 || ext.Publish[0] != (PublishSpec{Type: "pr.ready", Target: "niq"}) {
		t.Fatalf("unmanaged publish = %+v", ext.Publish)
	}
	if ext.Credential != "" {
		t.Fatal("credential must not enter the exported template")
	}
	if ext.Managed == nil || *ext.Managed {
		t.Fatal("unmanaged worker must stay unmanaged (nil counts as managed)")
	}
}

// TestExportProjectTemplateMissingConfig verifies a managed worker without a
// config.json fails the export (with the id) instead of exporting a hollow
// worker.
func TestExportProjectTemplateMissingConfig(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("alpha", fakeTemplate()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := os.Remove(filepath.Join(ProjectDir("alpha"), "workers", "niq", "config.json")); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	_, err := ExportProjectTemplate("alpha")
	if err == nil || !strings.Contains(err.Error(), "niq") {
		t.Fatalf("want an error naming the worker, got %v", err)
	}
}

// TestExportProjectTemplateEmpty verifies a project with no (non-archived)
// workers is an error, not an empty template file.
func TestExportProjectTemplateEmpty(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("alpha", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := ExportProjectTemplate("alpha"); err == nil {
		t.Fatal("exporting a workerless project should fail")
	}
}
