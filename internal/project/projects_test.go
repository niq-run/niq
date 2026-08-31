package project

import (
	"os"
	"path/filepath"
	"testing"
)

func setupProjectsRoot(t *testing.T) string {
	// Isolate the projects root under a temp ~/.niq by pointing HOME there,
	// so tests never touch a real ~/.niq/projects.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Existing ProjectPath/ListProjects use os.UserHomeDir(), which reads $HOME.
	return filepath.Join(home, ".niq", "projects")
}

// fakeTemplate returns a small TemplateConfig usable as a project template.
func fakeTemplate() *TemplateConfig {
	return &TemplateConfig{Workers: []WorkerConfig{
		{Type: "hiw", ID: "default-hiw"},
		{Type: "reason", ID: "niq", Provider: "volcan-ark", Model: "deepseek-v4-flash"},
	}}
}

func TestCreateLoadListProject(t *testing.T) {
	setupProjectsRoot(t)
	root := ProjectsRoot()

	if _, err := CreateProject("alpha", fakeTemplate()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// project.json + directory should exist under projects/<alpha>.
	if _, err := os.Stat(ProjectPath("alpha")); err != nil {
		t.Fatalf("project.json missing: %v", err)
	}
	if p, err := LoadProject("alpha"); err != nil {
		t.Fatalf("LoadProject: %v", err)
	} else if p.ID != "alpha" {
		t.Fatalf("project = %+v, want id alpha", p)
	}

	// Each template worker's authoritative config.json is seeded.
	for _, id := range []string{"default-hiw", "niq"} {
		cfg, ok := readWorkerConfig(ProjectDir("alpha"), id)
		if !ok {
			t.Fatalf("worker %s config.json not seeded", id)
		}
		if cfg.Type == "" {
			t.Fatalf("worker %s config has no type", id)
		}
	}
	// The reason worker's template params (provider/model) carried over.
	if cfg, ok := readWorkerConfig(ProjectDir("alpha"), "niq"); ok {
		if cfg.Params["provider"] != "volcan-ark" {
			t.Fatalf("niq provider = %v, want volcan-ark", cfg.Params["provider"])
		}
	}

	// Duplicate create must fail.
	if _, err := CreateProject("alpha", fakeTemplate()); err == nil {
		t.Fatal("expected error creating duplicate project")
	}

	// List includes alpha.
	projects, err := ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "alpha" {
		t.Fatalf("ListProjects = %+v, want [alpha]", projects)
	}
	if got := ProjectsRoot(); got != root {
		t.Fatalf("ProjectsRoot = %q, want %q", got, root)
	}
}

func TestSaveProjectMutatesArchived(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("beta", fakeTemplate()); err != nil {
		t.Fatal(err)
	}
	p, _ := LoadProject("beta")
	p.Archived = map[string]bool{"ws": true}
	if err := SaveProject(p); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := LoadProject("beta")
	if !reloaded.Archived["ws"] {
		t.Fatalf("archived = %+v, want ws=true", reloaded.Archived)
	}

	// Toggle off through the archiver.
	arc := projectArchiver{id: "beta"}
	if err := arc.SetArchived("ws", false); err != nil {
		t.Fatal(err)
	}
	if got := arc.Archived(); len(got) != 0 {
		t.Fatalf("Archived() = %+v, want empty", got)
	}
}

func TestCreateProjectUnmanagedWorker(t *testing.T) {
	setupProjectsRoot(t)
	tmpl := &TemplateConfig{Workers: []WorkerConfig{
		{Type: "reason", ID: "niq"},
		{Type: "mcp", ID: "mcp-fs", Managed: boolPtr(false),
			Command: []string{"npx", "mcp-fs"}, Env: map[string]string{"K": "V"}, Cwd: "/tmp"},
	}}
	if _, err := CreateProject("gamma", tmpl); err != nil {
		t.Fatal(err)
	}
	p, _ := LoadProject("gamma")
	if len(p.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(p.Workers))
	}
	var mcp *ProjectWorker
	for i := range p.Workers {
		if p.Workers[i].ID == "mcp-fs" {
			mcp = &p.Workers[i]
		}
	}
	if mcp == nil || mcp.Managed {
		t.Fatalf("mcp-fs entry = %+v, want unmanaged", mcp)
	}
	if len(mcp.Command) != 2 || mcp.Cwd != "/tmp" || mcp.Env["K"] != "V" {
		t.Fatalf("mcp-fs launch spec = %+v", mcp)
	}
	// The managed worker still gets its authoritative worker-dir config.
	if _, ok := readWorkerConfig(ProjectDir("gamma"), "niq"); !ok {
		t.Fatal("niq config not seeded to worker dir")
	}
	// Unmanaged workers are not seeded to the worker dir.
	unm := UnmanagedWorkers(p)
	if len(unm) != 1 || unm[0].ID != "mcp-fs" {
		t.Fatalf("UnmanagedWorkers = %+v", unm)
	}
}

func TestListProjectsEmpty(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := ListProjects(); err != nil {
		t.Fatalf("ListProjects on empty root: %v", err)
	}
}

func TestSanitizeID(t *testing.T) {
	if got := sanitizeID("my proj/1"); got != "my_proj_1" {
		t.Fatalf("sanitize = %q, want my_proj_1", got)
	}
	if got := sanitizeID("ok-id_1.x"); got != "ok-id_1.x" {
		t.Fatalf("sanitize = %q, want ok-id_1.x", got)
	}
}
