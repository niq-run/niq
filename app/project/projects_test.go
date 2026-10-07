package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/niqhome"
)

func setupProjectsRoot(t *testing.T) string {
	// Isolate the projects root under a temp ~/.niq by pointing HOME there,
	// so tests never touch a real ~/.niq/projects. Ensure a stray NIQ_HOME
	// can't override that relocation.
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.Unsetenv(niqhome.Env)
	return filepath.Join(home, ".niq", "projects")
}

func TestProjectsRootHonorsNIQHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(niqhome.Env, filepath.Join(t.TempDir(), "custom-root"))

	projects := ProjectsRoot()
	if _, err := CreateProject("beta", "", fakeTemplate()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := os.Stat(projects); err != nil {
		t.Fatalf("ProjectsRoot not created under NIQ_HOME: %v", err)
	}
	if _, err := os.Stat(ProjectPath("beta")); err != nil {
		t.Fatalf("project.json missing under NIQ_HOME: %v", err)
	}
	// The default ~/.niq must stay untouched.
	if _, err := os.Stat(filepath.Join(home, ".niq")); !os.IsNotExist(err) {
		t.Fatalf("default ~/.niq was touched: %v", err)
	}
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

	if _, err := CreateProject("alpha", "", fakeTemplate()); err != nil {
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

	// Each template worker is declared with its full config in project.json.
	created, err := LoadProject("alpha")
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	for _, id := range []string{"default-hiw", "niq"} {
		wc, ok := FindWorker(created, id)
		if !ok {
			t.Fatalf("worker %s not declared", id)
		}
		if wc.Type == "" {
			t.Fatalf("worker %s declaration has no type", id)
		}
	}
	// The reason worker's template config (provider/model) carried over.
	if wc, ok := FindWorker(created, "niq"); ok {
		if wc.Provider != "volcan-ark" || wc.Model != "deepseek-v4-flash" {
			t.Fatalf("niq provider/model = %v/%v, want volcan-ark/deepseek-v4-flash", wc.Provider, wc.Model)
		}
	}

	// Duplicate create must fail.
	if _, err := CreateProject("alpha", "", fakeTemplate()); err == nil {
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

// TestCreateProjectExpandsPlaceholder verifies the {project} template
// placeholder is resolved once, when the declaration is instantiated into
// project.json — the runtime never sees it.
func TestCreateProjectExpandsPlaceholder(t *testing.T) {
	setupProjectsRoot(t)
	tpl := &TemplateConfig{Workers: []WorkerConfig{
		{Type: "workspace", ID: "ws", Mounts: []string{"~/.niq/projects/{project}/workspace"}},
	}}
	if _, err := CreateProject("alpha", "", tpl); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	p, err := LoadProject("alpha")
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	wc, ok := FindWorker(p, "ws")
	if !ok {
		t.Fatal("ws not declared in project.json")
	}
	if len(wc.Mounts) != 1 || wc.Mounts[0] != "~/.niq/projects/alpha/workspace" {
		t.Fatalf("declared mounts = %v, want [~/.niq/projects/alpha/workspace]", wc.Mounts)
	}
	if strings.Contains(fmt.Sprint(wc.Mounts), "{project}") {
		t.Fatalf("placeholder leaked into the declaration: %v", wc.Mounts)
	}
}

func TestSaveProjectMutatesArchived(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("beta", "", fakeTemplate()); err != nil {
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
	if _, err := CreateProject("gamma", "", tmpl); err != nil {
		t.Fatal(err)
	}
	p, _ := LoadProject("gamma")
	if len(p.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(p.Workers))
	}
	var mcp *WorkerConfig
	for i := range p.Workers {
		if p.Workers[i].ID == "mcp-fs" {
			mcp = &p.Workers[i]
		}
	}
	if mcp == nil || isManagedWorker(*mcp) {
		t.Fatalf("mcp-fs entry = %+v, want unmanaged", mcp)
	}
	if len(mcp.Command) != 2 || mcp.Cwd != "/tmp" || mcp.Env["K"] != "V" {
		t.Fatalf("mcp-fs launch spec = %+v", mcp)
	}
	// The managed worker is declared in project.json, not in a worker dir.
	if wc, ok := FindWorker(p, "niq"); !ok || !isManagedWorker(wc) {
		t.Fatalf("niq declaration = %+v, want managed", wc)
	}
	// Unmanaged workers are the only ones handed to the supervisor.
	unm := UnmanagedWorkers(p)
	if len(unm) != 1 || unm[0].ID != "mcp-fs" {
		t.Fatalf("UnmanagedWorkers = %+v", unm)
	}
}

// TestCreateProjectSeedsTemplatePrograms verifies a named template's programs
// resources are copied into the new project's programs/ directory.
func TestCreateProjectSeedsTemplatePrograms(t *testing.T) {
	setupProjectsRoot(t)
	tmplDir := TemplateDir(TemplatesDir(), "prog")
	if err := os.MkdirAll(TemplateDir(tmplDir, ""), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(TemplatePath(TemplatesDir(), "prog"), []byte(`{"workers":[{"type":"reason","id":"niq"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(tmplDir, "programs", "review")
	if err := os.MkdirAll(prog, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prog, "SKILL.md"), []byte("---\nname: review\n---\n# Review"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := CreateProject("alpha", "prog", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(ProjectProgramsDir("alpha"), "review", "SKILL.md"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("template programs not seeded into project: %v", err)
	}
	// A project created without a template name gets nothing copied.
	if _, err := CreateProject("beta", "", nil); err != nil {
		t.Fatalf("CreateProject(beta): %v", err)
	}
	if _, err := os.Stat(ProjectProgramsDir("beta")); !os.IsNotExist(err) {
		t.Fatalf("project without a template should have no programs dir, err=%v", err)
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

// TestLinkProjectExisting verifies opening a directory that is already a niq
// project: the link keeps the project's own id, ListProjects sees through the
// symlink, the original project.json is preserved, and a duplicate is rejected.
func TestLinkProjectExisting(t *testing.T) {
	setupProjectsRoot(t)

	ext := t.TempDir()
	proj := &Project{ID: "alpha", CreatedAt: "2026-01-01T00:00:00Z",
		Workers: []WorkerConfig{{Type: "hiw", ID: "default-hiw"}}}
	if err := writeProjectJSON(filepath.Join(ext, "project.json"), proj); err != nil {
		t.Fatalf("seed project.json: %v", err)
	}

	p, err := LinkProject(ext)
	if err != nil {
		t.Fatalf("LinkProject: %v", err)
	}
	if p.ID != "alpha" {
		t.Fatalf("linked id = %q, want alpha", p.ID)
	}

	// A symlink must exist under the niq root, pointing back to ext.
	link := ProjectDir("alpha")
	if info, err := os.Lstat(link); err != nil {
		t.Fatalf("link missing: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected a symlink at %s", link)
	}
	if target, err := os.Readlink(link); err != nil || filepath.Clean(target) != filepath.Clean(ext) {
		t.Fatalf("link target = %q, want %q (%v)", target, ext, err)
	}

	// The project is now scannable (ListProjects must see through the symlink).
	projects, err := ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "alpha" {
		t.Fatalf("ListProjects = %+v, want [alpha]", projects)
	}
	// The original project.json is left in place.
	if _, err := os.Stat(filepath.Join(ext, "project.json")); err != nil {
		t.Fatalf("original project.json missing: %v", err)
	}

	// Linking the same directory again must fail with a duplicate-id error.
	if _, err := LinkProject(ext); err == nil {
		t.Fatal("expected duplicate link error")
	}
}

// TestLinkProjectCreatesProjectJSON verifies opening an arbitrary directory
// derives the id from its base name and seeds a minimal project.json so the
// project scans and can run with the webui-hiw.
func TestLinkProjectCreatesProjectJSON(t *testing.T) {
	setupProjectsRoot(t)
	ext := t.TempDir() // arbitrary directory, no project.json

	p, err := LinkProject(ext)
	if err != nil {
		t.Fatalf("LinkProject: %v", err)
	}
	base := sanitizeID(filepath.Base(ext))
	if p.ID != base {
		t.Fatalf("linked id = %q, want %q", p.ID, base)
	}
	if _, err := os.Stat(filepath.Join(ext, "project.json")); err != nil {
		t.Fatalf("project.json not seeded: %v", err)
	}
	if list, err := ListProjects(); err != nil || len(list) != 1 || list[0].ID != base {
		t.Fatalf("ListProjects after link = %+v, want [%s] (err %v)", list, base, err)
	}
}

func TestLinkProjectRejectsInvalid(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := LinkProject(""); err == nil {
		t.Fatal("expected error for empty path")
	}
	// A plain file is not a directory.
	f := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkProject(f); err == nil {
		t.Fatal("expected error linking a file")
	}
}
