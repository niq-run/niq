package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/app/standalone/project/store"
	projworker "github.com/niq-run/niq/app/standalone/worker"
)

func exportTemplate() *TemplateConfig {
	return &TemplateConfig{SchemaVersion: SchemaVersion, Workers: []WorkerConfig{
		{
			Definition: projworker.Definition{
				Type: "reason", ID: "niq",
				Params: map[string]any{
					"instruction": "do the thing",
					"provider":    "volcan-ark",
					"model":       "deepseek-v4-flash",
					"mounts":      []any{"/tmp/demo"},
				},
			},
			Placement: projworker.PlacementInProcess,
		},
		{
			Definition: projworker.Definition{Type: "reason", ID: "retired", Params: map[string]any{"instruction": "this one gets archived"}},
			Placement:  projworker.PlacementInProcess,
		},
		{
			Definition: projworker.Definition{Type: "http", ID: "ext-1", Params: map[string]any{"PORT": "8080"}},
			Placement:  projworker.PlacementManagedProcess,
			Command:    []string{"./worker-ts", "run"},
			Cwd:        "/tmp/ext",
		},
	}}
}

// TestExportProjectTemplateRoundTrip creates a project, archives one worker,
// exports it back, and checks that params and the launch spec survive while the
// archived worker does not.
func TestExportProjectTemplateRoundTrip(t *testing.T) {
	setupProjectsRoot(t)
	src := exportTemplate()
	if _, err := CreateProject("alpha", "", src); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := store.SetArchived(ProjectDir("alpha"), "retired", true); err != nil {
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
}

// TestExportProjectTemplateParams verifies params travel verbatim.
func TestExportProjectTemplateParams(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("alpha", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	d := projworker.Manifest{Definition: projworker.Definition{Type: "reason", ID: "niq", Params: map[string]any{
		"programs": []any{map[string]any{
			"name": "review", "content_type": "playbook", "content": "# Review\n...",
		}},
	}}}
	if err := store.NewManifest(ProjectDir("alpha")).Add(&d); err != nil {
		t.Fatalf("Add: %v", err)
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
