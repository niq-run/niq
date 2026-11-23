package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/store"
	projtemplate "github.com/niq-run/niq/app/standalone/template"
	projworker "github.com/niq-run/niq/app/standalone/worker"
	"github.com/niq-run/niq/core/impl/workers/program/pgbackend"
)

// loadWorkers / loadArchived are the project-scoped shortcuts the tests read.
func loadWorkers(id string) (*store.WorkerSet, error) { return store.LoadWorkers(store.ProjectDir(id)) }
func loadArchived(id string) (*store.WorkerSet, error) {
	return store.LoadArchived(store.ProjectDir(id))
}
func workersPath(id string) string  { return store.WorkersPath(store.ProjectDir(id)) }
func archivedPath(id string) string { return store.ArchivedPath(store.ProjectDir(id)) }
func setArchived(id, workerID string, archived bool) error {
	return store.SetArchived(store.ProjectDir(id), workerID, archived)
}

func setupProjectsRoot(t *testing.T) string {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	_ = os.Unsetenv(store.Env)
	return filepath.Join(tmpHome, ".niq", "projects")
}

func TestProjectsRootHonorsNIQHome(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv(store.Env, filepath.Join(t.TempDir(), "custom-root"))

	projects := store.ProjectsRoot()
	if _, err := CreateProject("beta", "", fakeTemplate()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := os.Stat(projects); err != nil {
		t.Fatalf("ProjectsRoot not created under NIQ_HOME: %v", err)
	}
	if _, err := os.Stat(store.ProjectPath("beta")); err != nil {
		t.Fatalf("project.json missing under NIQ_HOME: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpHome, ".niq")); !os.IsNotExist(err) {
		t.Fatalf("default ~/.niq was touched: %v", err)
	}
}

func fakeTemplate() *TemplateConfig {
	return &TemplateConfig{SchemaVersion: SchemaVersion, Workers: []WorkerConfig{
		{Definition: projworker.Definition{Type: "interface", ID: "default-ui"}},
		{Definition: projworker.Definition{Type: "reason", ID: "niq", Params: map[string]any{
			"provider": "volcan-ark", "model": "deepseek-flash",
		}}, Placement: projworker.PlacementInProcess},
	}}
}

func TestCreateLoadListProject(t *testing.T) {
	setupProjectsRoot(t)
	root := store.ProjectsRoot()
	if _, err := CreateProject("alpha", "", fakeTemplate()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := os.Stat(store.ProjectPath("alpha")); err != nil {
		t.Fatalf("project.json missing: %v", err)
	}
	if _, err := os.Stat(workersPath("alpha")); err != nil {
		t.Fatalf("workers.json missing: %v", err)
	}
	p, err := LoadProject("alpha")
	if err != nil || p.ID != "alpha" {
		t.Fatalf("LoadProject = %+v (%v)", p, err)
	}
	set, err := loadWorkers("alpha")
	if err != nil {
		t.Fatalf("LoadWorkers: %v", err)
	}
	for _, id := range []string{"default-ui", "niq"} {
		if _, ok := store.FindWorker(set, id); !ok {
			t.Fatalf("worker %s not declared", id)
		}
	}
	if wc, ok := store.FindWorker(set, "niq"); ok {
		if wc.Params["provider"] != "volcan-ark" || wc.Params["model"] != "deepseek-flash" {
			t.Fatalf("niq params = %v", wc.Params)
		}
	}
	if _, err := CreateProject("alpha", "", fakeTemplate()); err == nil {
		t.Fatal("expected error creating duplicate project")
	}
	projects, err := ListProjects()
	if err != nil || len(projects) != 1 || projects[0].ID != "alpha" {
		t.Fatalf("ListProjects = %+v (%v)", projects, err)
	}
	if got := store.ProjectsRoot(); got != root {
		t.Fatalf("ProjectsRoot = %q, want %q", got, root)
	}
}

func TestCreateProjectExpandsPlaceholder(t *testing.T) {
	setupProjectsRoot(t)
	tpl := &TemplateConfig{Workers: []WorkerConfig{
		{Definition: projworker.Definition{Type: "workspace", ID: "ws", Params: map[string]any{
			"mounts": []any{"~/.niq/projects/{project}/workspace"},
		}}, Placement: projworker.PlacementInProcess},
	}}
	if _, err := CreateProject("alpha", "", tpl); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	set, err := loadWorkers("alpha")
	if err != nil {
		t.Fatal(err)
	}
	wc, ok := store.FindWorker(set, "ws")
	if !ok {
		t.Fatal("ws not declared")
	}
	mounts, _ := wc.Params["mounts"].([]any)
	if len(mounts) != 1 || mounts[0] != "~/.niq/projects/alpha/workspace" {
		t.Fatalf("mounts = %v", wc.Params)
	}
	if strings.Contains(fmt.Sprint(wc.Params), "{project}") {
		t.Fatalf("placeholder leaked: %v", wc.Params)
	}
}

func TestArchiveMovesDeclaration(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("beta", "", fakeTemplate()); err != nil {
		t.Fatal(err)
	}
	if err := setArchived("beta", "niq", true); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ArchivedIDs(store.ProjectDir("beta")); err != nil || len(got) != 1 || got[0] != "niq" {
		t.Fatalf("ArchivedWorkerIDs() = %+v, %v", got, err)
	}
	active, _ := loadWorkers("beta")
	if _, ok := store.FindWorker(active, "niq"); ok {
		t.Fatal("archived worker still in active set")
	}
	arch, _ := loadArchived("beta")
	if wc, ok := store.FindWorker(arch, "niq"); !ok || wc.Params["provider"] != "volcan-ark" {
		t.Fatalf("archived declaration = %+v", wc)
	}
	if err := setArchived("beta", "niq", false); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ArchivedIDs(store.ProjectDir("beta")); err != nil || len(got) != 0 {
		t.Fatalf("ArchivedWorkerIDs() = %+v, %v, want empty", got, err)
	}
	restored, _ := loadWorkers("beta")
	if _, ok := store.FindWorker(restored, "niq"); !ok {
		t.Fatal("unarchived worker is not active again")
	}
}

func TestCreateProjectProcessWorkers(t *testing.T) {
	setupProjectsRoot(t)
	tmpl := &TemplateConfig{Workers: []WorkerConfig{
		{Definition: projworker.Definition{Type: "reason", ID: "niq"}},
		{Definition: projworker.Definition{Type: "mcp", ID: "mcp-fs", Params: map[string]any{"K": "V"}},
			Placement: projworker.PlacementManagedProcess,
			Command:   []string{"npx", "mcp-fs"}, Cwd: "/tmp"},
		{Definition: projworker.Definition{Type: "mcp", ID: "remote"},
			Placement: projworker.PlacementUnmanagedProcess},
	}}
	if _, err := CreateProject("gamma", "", tmpl); err != nil {
		t.Fatal(err)
	}
	set, _ := loadWorkers("gamma")
	if len(set.Workers) != 3 {
		t.Fatalf("workers = %d", len(set.Workers))
	}
	mcp, ok := store.FindWorker(set, "mcp-fs")
	if !ok || projworker.PlacementOf(mcp) != projworker.PlacementManagedProcess {
		t.Fatalf("mcp-fs = %+v", mcp)
	}
	if len(mcp.Command) != 2 || mcp.Cwd != "/tmp" || mcp.Params["K"] != "V" {
		t.Fatalf("mcp-fs launch spec = %+v", mcp)
	}
	if p := projworker.ProcessDeclarations(set.Workers); len(p) != 2 {
		t.Fatalf("ProcessDeclarations = %+v", p)
	}
}

func TestCreateProjectSeedsTemplatePrograms(t *testing.T) {
	setupProjectsRoot(t)
	tmplDir := projtemplate.Dir(projtemplate.TemplatesDir(), "prog")
	if err := os.MkdirAll(tmplDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projtemplate.Path(projtemplate.TemplatesDir(), "prog"), []byte(`{"schema_version":2,"workers":[{"type":"reason","id":"niq"}]}`), 0644); err != nil {
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
		t.Fatalf("template programs not seeded: %v", err)
	}
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
	if got := store.SanitizeID("my proj/1"); got != "my_proj_1" {
		t.Fatalf("sanitize = %q", got)
	}
	if got := store.SanitizeID("ok-id_1.x"); got != "ok-id_1.x" {
		t.Fatalf("sanitize = %q", got)
	}
}

func TestLinkProjectExisting(t *testing.T) {
	setupProjectsRoot(t)
	ext := t.TempDir()
	if err := store.SaveProject(ext, &store.Project{ID: "alpha", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("seed project.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ext, "workers.json"), []byte(`{"schema_version":2,"workers":[{"id":"default-ui","type":"interface"}]}`), 0644); err != nil {
		t.Fatalf("seed workers: %v", err)
	}
	p, err := LinkProject(ext)
	if err != nil || p.ID != "alpha" {
		t.Fatalf("LinkProject = %+v (%v)", p, err)
	}
	link := store.ProjectDir("alpha")
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink at %s (%v)", link, err)
	}
	if target, err := os.Readlink(link); err != nil || filepath.Clean(target) != filepath.Clean(ext) {
		t.Fatalf("link target = %q (%v)", target, err)
	}
	projects, err := ListProjects()
	if err != nil || len(projects) != 1 || projects[0].ID != "alpha" {
		t.Fatalf("ListProjects = %+v (%v)", projects, err)
	}
	if _, err := LinkProject(ext); err == nil {
		t.Fatal("expected duplicate link error")
	}
}

func TestLinkProjectCreatesProjectJSON(t *testing.T) {
	setupProjectsRoot(t)
	ext := t.TempDir()
	p, err := LinkProject(ext)
	if err != nil {
		t.Fatalf("LinkProject: %v", err)
	}
	base := store.SanitizeID(filepath.Base(ext))
	if p.ID != base {
		t.Fatalf("linked id = %q, want %q", p.ID, base)
	}
	if _, err := os.Stat(filepath.Join(ext, "project.json")); err != nil {
		t.Fatalf("project.json not seeded: %v", err)
	}
}

func TestLinkProjectRejectsInvalid(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := LinkProject(""); err == nil {
		t.Fatal("expected error for empty path")
	}
	f := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkProject(f); err == nil {
		t.Fatal("expected error linking a file")
	}
}

func TestCreateProjectSeedsBuiltinPrograms(t *testing.T) {
	setupProjectsRoot(t)
	def, err := projtemplate.LoadPreset("default")
	if err != nil {
		t.Fatalf("LoadPreset(default): %v", err)
	}
	if _, err := CreateProject("alpha", "default", def); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(ProjectProgramsDir("alpha"), "using-niq", "PROGRAM.md"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("built-in playbook not seeded: %v", err)
	}
}

func TestBuiltinPlaybookIsLoadable(t *testing.T) {
	setupProjectsRoot(t)
	def, err := projtemplate.LoadPreset("default")
	if err != nil {
		t.Fatalf("LoadPreset(default): %v", err)
	}
	if _, err := CreateProject("alpha", "default", def); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	be := pgbackend.New(ProjectProgramsDir("alpha"))
	progs, err := be.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var guide, cheat string
	for _, p := range progs {
		if p.Name != "using-niq" {
			continue
		}
		for _, c := range p.Contents {
			if c.Path == "using-niq/CHEATSHEET.md" {
				cheat = c.Path
			}
		}
		guide = p.Path
	}
	if guide != "using-niq" || cheat == "" {
		t.Fatalf("programs = %+v", progs)
	}
}

func TestDefaultPresetShipsUsingNiqPlaybook(t *testing.T) {
	def, err := projtemplate.LoadPreset("default")
	if err != nil {
		t.Fatalf("LoadPreset(default): %v", err)
	}
	var niq *WorkerConfig
	for i := range def.Workers {
		if def.Workers[i].ID == "niq" && def.Workers[i].Type == "reason" {
			niq = &def.Workers[i]
		}
	}
	if niq == nil {
		t.Fatal("default preset has no reason worker niq")
	}
	raw, _ := niq.Params["programs"].([]any)
	if len(raw) != 1 {
		t.Fatalf("programs = %+v", niq.Params)
	}
	progs := projworker.ParsePrograms(niq.Params, "niq")
	var seen bool
	for _, p := range progs {
		if p.Name == "using-niq" && p.ContentType == "playbook" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("ParsePrograms = %+v", progs)
	}
	if _, err := projtemplate.ReadPresetFile("default/programs/using-niq/PROGRAM.md"); err != nil {
		t.Fatalf("embedded PROGRAM.md: %v", err)
	}
}

// TestLinkProjectMigratesLegacy verifies linking a legacy external project
// converts it before project.json is rewritten, so declarations are not lost.
func TestLinkProjectMigratesLegacy(t *testing.T) {
	setupProjectsRoot(t)
	ext := t.TempDir()
	legacy := `{"id":"legacy","created_at":"2026-01-01T00:00:00Z","workers":[{"type":"reason","id":"niq","instruction":"do x"}],"archived":{"old":true}}`
	if err := os.WriteFile(filepath.Join(ext, "project.json"), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkProject(ext); err != nil {
		t.Fatalf("LinkProject: %v", err)
	}
	set, err := loadWorkers("legacy")
	if err != nil {
		t.Fatalf("LoadWorkers: %v", err)
	}
	if wc, ok := store.FindWorker(set, "niq"); !ok || wc.Params["instruction"] != "do x" {
		t.Fatalf("legacy declaration lost: %+v", wc)
	}
	if _, err := os.Stat(archivedPath("legacy")); err != nil {
		t.Fatalf("archived file missing: %v", err)
	}
}
