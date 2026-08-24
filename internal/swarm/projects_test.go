package swarm

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

// fakeTemplate returns a small SwarmConfig usable as a project template.
func fakeTemplate() *SwarmConfig {
	return &SwarmConfig{Workers: []WorkerConfig{
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
	} else if len(p.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(p.Workers))
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

func TestSaveProjectMutatesWorkers(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("beta", fakeTemplate()); err != nil {
		t.Fatal(err)
	}
	p, _ := LoadProject("beta")
	p.Workers = append(p.Workers, ProjectWorker{Type: "workspace", ID: "ws"})
	if err := SaveProject(p); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := LoadProject("beta")
	if len(reloaded.Workers) != 3 {
		t.Fatalf("workers = %d, want 3 after save", len(reloaded.Workers))
	}
}

func TestListProjectsEmpty(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := ListProjects(); err != nil {
		t.Fatalf("ListProjects on empty root: %v", err)
	}
}

func TestSanitizeProjectID(t *testing.T) {
	if got := sanitizeProjectID("my proj/1"); got != "my_proj_1" {
		t.Fatalf("sanitize = %q, want my_proj_1", got)
	}
	if got := sanitizeProjectID("ok-id_1.x"); got != "ok-id_1.x" {
		t.Fatalf("sanitize = %q, want ok-id_1.x", got)
	}
}