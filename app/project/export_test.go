package project

import (
	"os"
	"path/filepath"
	"testing"

	projworker "github.com/niq-run/niq/app/project/worker"
)

func exportTemplate() *TemplateConfig {
	return &TemplateConfig{SchemaVersion: SchemaVersion, Workers: []WorkerConfig{
		{
			Config: projworker.Config{
				Type: "reason", ID: "niq",
				Params: map[string]any{
					"instruction": "do the thing",
					"provider":    "volcan-ark",
					"model":       "deepseek-v4-flash",
					"mounts":      []any{"/tmp/demo"},
				},
			},
			Placement:     projworker.PlacementInProcess,
			Subscriptions: []SubscriptionSpec{{Type: "worker.input", Source: "timer"}},
			Publish:       []PublishSpec{{Type: "reason.response"}},
		},
		{
			Config:    projworker.Config{Type: "reason", ID: "retired", Params: map[string]any{"instruction": "this one gets archived"}},
			Placement: projworker.PlacementInProcess,
		},
		{
			Config:     projworker.Config{Type: "http", ID: "ext-1", Params: map[string]any{"PORT": "8080"}},
			Placement:  projworker.PlacementManagedProcess,
			Credential: "tok-secret",
			Command:    []string{"./worker-ts", "run"},
			Cwd:        "/tmp/ext",
			Publish:    []PublishSpec{{Type: "pr.ready", Target: "niq"}},
		},
	}}
}

// TestExportProjectTemplateRoundTrip creates a project, archives one worker,
// exports it back, and checks that params/ACL/launch spec survive while the
// archived worker and secrets do not.
func TestExportProjectTemplateRoundTrip(t *testing.T) {
	setupProjectsRoot(t)
	src := exportTemplate()
	if _, err := CreateProject("alpha", "", src); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	arch := projectArchiver{id: "alpha"}
	if err := arch.SetArchived("retired", true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	out, err := ExportProjectTemplate("alpha", false)
	if err != nil {
		t.Fatalf("ExportProjectTemplate: %v", err)
	}
	if len(out.Workers) != 2 {
		t.Fatalf("exported %d workers, want 2 (archived skipped): %+v", len(out.Workers), out.Workers)
	}

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
	if niq.Params["instruction"] != want.Params["instruction"] ||
		niq.Params["provider"] != want.Params["provider"] ||
		niq.Params["model"] != want.Params["model"] {
		t.Fatalf("niq params drifted:\n got %+v\nwant %+v", niq.Params, want.Params)
	}
	if len(niq.Subscriptions) != 1 || niq.Subscriptions[0] != want.Subscriptions[0] {
		t.Fatalf("niq subscriptions = %+v, want %+v", niq.Subscriptions, want.Subscriptions)
	}
	if len(niq.Publish) != 1 || niq.Publish[0] != want.Publish[0] {
		t.Fatalf("niq publish = %+v, want %+v", niq.Publish, want.Publish)
	}

	var ext WorkerConfig
	for _, w := range out.Workers {
		if w.ID == "ext-1" {
			ext = w
		}
	}
	if ext.ID == "" {
		t.Fatal("unmanaged worker ext-1 missing from export")
	}
	if len(ext.Command) != 2 || ext.Params["PORT"] != "8080" || ext.Cwd != "/tmp/ext" {
		t.Fatalf("launch spec drifted: %+v", ext)
	}
	if ext.Credential != "" {
		t.Fatal("credential must not enter the exported template")
	}
}

// TestExportProjectTemplateParams verifies params travel verbatim.
func TestExportProjectTemplateParams(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("alpha", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := AppendWorkerDecl("alpha", WorkerConfig{Config: projworker.Config{Type: "reason", ID: "niq", Params: map[string]any{
		"programs": []any{map[string]any{
			"name": "review", "content_type": "playbook", "content": "# Review\n...",
		}},
	}}}); err != nil {
		t.Fatalf("AppendWorkerDecl: %v", err)
	}
	out, err := ExportProjectTemplate("alpha", false)
	if err != nil {
		t.Fatalf("ExportProjectTemplate: %v", err)
	}
	raw, _ := out.Workers[0].Params["programs"].([]any)
	if len(raw) != 1 {
		t.Fatalf("programs = %+v", out.Workers[0].Params)
	}
}

// TestExportProjectPrograms verifies the programs resources are copied into a
// template directory, and a project without programs is a no-op.
func TestExportProjectPrograms(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("alpha", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tmplDir := t.TempDir()
	copied, err := ExportProjectPrograms("alpha", tmplDir)
	if err != nil || copied {
		t.Fatalf("no-programs project: copied=%v err=%v, want false/nil", copied, err)
	}
	src := ProjectProgramsDir("alpha")
	if err := os.MkdirAll(filepath.Join(src, "review"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "review", "SKILL.md"), []byte("---\nname: review\n---\n# Review"), 0644); err != nil {
		t.Fatal(err)
	}
	copied, err = ExportProjectPrograms("alpha", tmplDir)
	if err != nil || !copied {
		t.Fatalf("copy: copied=%v err=%v, want true/nil", copied, err)
	}
	raw, err := os.ReadFile(filepath.Join(tmplDir, "programs", "review", "SKILL.md"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("programs not copied: %v", err)
	}
}

// TestExportProjectTemplateEmpty verifies a project with no active workers is an
// error, not an empty template file.
func TestExportProjectTemplateEmpty(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("alpha", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := ExportProjectTemplate("alpha", false); err == nil {
		t.Fatal("exporting a workerless project should fail")
	}
}
