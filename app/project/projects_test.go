package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/niqhome"
	projtemplate "github.com/niq-run/niq/app/project/template"
	projworker "github.com/niq-run/niq/app/project/worker"
	"github.com/niq-run/niq/core/impl/workers/program/pgbackend"
)

func setupProjectsRoot(t *testing.T) string {
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
	if _, err := os.Stat(filepath.Join(home, ".niq")); !os.IsNotExist(err) {
		t.Fatalf("default ~/.niq was touched: %v", err)
	}
}

func fakeTemplate() *TemplateConfig {
	return &TemplateConfig{SchemaVersion: SchemaVersion, Workers: []WorkerConfig{
		{Config: projworker.Config{Type: "hiw", ID: "default-hiw"}},
		{Config: projworker.Config{Type: "reason", ID: "niq", Params: map[string]any{
			"provider": "volcan-ark", "model": "deepseek-v4-flash",
		}}, Placement: projworker.PlacementInProcess},
	}}
}

func TestCreateLoadListProject(t *testing.T) {
	setupProjectsRoot(t)
	root := ProjectsRoot()
	if _, err := CreateProject("alpha", "", fakeTemplate()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := os.Stat(ProjectPath("alpha")); err != nil {
		t.Fatalf("project.json missing: %v", err)
	}
	if _, err := os.Stat(WorkersPath("alpha")); err != nil {
		t.Fatalf("workers.json missing: %v", err)
	}
	p, err := LoadProject("alpha")
	if err != nil || p.ID != "alpha" {
		t.Fatalf("LoadProject = %+v (%v)", p, err)
	}
	set, err := LoadWorkers("alpha")
	if err != nil {
		t.Fatalf("LoadWorkers: %v", err)
	}
	for _, id := range []string{"default-hiw", "niq"} {
		if _, ok := FindWorker(set, id); !ok {
			t.Fatalf("worker %s not declared", id)
		}
	}
	if wc, ok := FindWorker(set, "niq"); ok {
		if wc.Params["provider"] != "volcan-ark" || wc.Params["model"] != "deepseek-v4-flash" {
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
	if got := ProjectsRoot(); got != root {
		t.Fatalf("ProjectsRoot = %q, want %q", got, root)
	}
}

func TestCreateProjectExpandsPlaceholder(t *testing.T) {
	setupProjectsRoot(t)
	tpl := &TemplateConfig{Workers: []WorkerConfig{
		{Config: projworker.Config{Type: "workspace", ID: "ws", Params: map[string]any{
			"mounts": []any{"~/.niq/projects/{project}/workspace"},
		}}, Placement: projworker.PlacementInProcess},
	}}
	if _, err := CreateProject("alpha", "", tpl); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	set, err := LoadWorkers("alpha")
	if err != nil {
		t.Fatal(err)
	}
	wc, ok := FindWorker(set, "ws")
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
	arc := projectArchiver{id: "beta"}
	if err := arc.SetArchived("niq", true); err != nil {
		t.Fatal(err)
	}
	if got := arc.Archived(); len(got) != 1 || got[0] != "niq" {
		t.Fatalf("Archived() = %+v", got)
	}
	active, _ := LoadWorkers("beta")
	if _, ok := FindWorker(active, "niq"); ok {
		t.Fatal("archived worker still in active set")
	}
	arch, _ := LoadArchived("beta")
	if wc, ok := FindWorker(arch, "niq"); !ok || wc.Params["provider"] != "volcan-ark" {
		t.Fatalf("archived declaration = %+v", wc)
	}
	if err := arc.SetArchived("niq", false); err != nil {
		t.Fatal(err)
	}
	if got := arc.Archived(); len(got) != 0 {
		t.Fatalf("Archived() = %+v, want empty", got)
	}
}

func TestCreateProjectProcessWorkers(t *testing.T) {
	setupProjectsRoot(t)
	tmpl := &TemplateConfig{Workers: []WorkerConfig{
		{Config: projworker.Config{Type: "reason", ID: "niq"}},
		{Config: projworker.Config{Type: "mcp", ID: "mcp-fs", Params: map[string]any{"K": "V"}},
			Placement: projworker.PlacementManagedProcess,
			Command:   []string{"npx", "mcp-fs"}, Cwd: "/tmp"},
		{Config: projworker.Config{Type: "mcp", ID: "remote"},
			Placement: projworker.PlacementUnmanagedProcess},
	}}
	if _, err := CreateProject("gamma", "", tmpl); err != nil {
		t.Fatal(err)
	}
	set, _ := LoadWorkers("gamma")
	if len(set.Workers) != 3 {
		t.Fatalf("workers = %d", len(set.Workers))
	}
	mcp, ok := FindWorker(set, "mcp-fs")
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
	if got := sanitizeID("my proj/1"); got != "my_proj_1" {
		t.Fatalf("sanitize = %q", got)
	}
	if got := sanitizeID("ok-id_1.x"); got != "ok-id_1.x" {
		t.Fatalf("sanitize = %q", got)
	}
}

func TestLinkProjectExisting(t *testing.T) {
	setupProjectsRoot(t)
	ext := t.TempDir()
	if err := writeProjectJSON(filepath.Join(ext, "project.json"), &Project{ID: "alpha", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("seed project.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ext, "workers.json"), []byte(`{"schema_version":2,"workers":[{"id":"default-hiw","type":"hiw"}]}`), 0644); err != nil {
		t.Fatalf("seed workers: %v", err)
	}
	p, err := LinkProject(ext)
	if err != nil || p.ID != "alpha" {
		t.Fatalf("LinkProject = %+v (%v)", p, err)
	}
	link := ProjectDir("alpha")
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
	base := sanitizeID(filepath.Base(ext))
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

func TestUpdateWorkerParams(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := CreateProject("proj", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := AppendWorkerDecl("proj", WorkerConfig{Config: projworker.Config{
		Type: "mcp", ID: "m", Params: map[string]any{"expose": "agentic"},
	}}); err != nil {
		t.Fatalf("AppendWorkerDecl: %v", err)
	}
	if err := UpdateWorkerParams("proj", "m", map[string]any{"fixed": true, "expose": "ready"}); err != nil {
		t.Fatalf("UpdateWorkerParams: %v", err)
	}
	set, _ := LoadWorkers("proj")
	wc, ok := FindWorker(set, "m")
	if !ok || wc.Params["fixed"] != true || wc.Params["expose"] != "ready" {
		t.Fatalf("params = %+v", wc.Params)
	}
	if err := UpdateWorkerParams("proj", "m", map[string]any{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	set, _ = LoadWorkers("proj")
	if wc, _ := FindWorker(set, "m"); wc.Params != nil {
		t.Fatalf("params = %+v, want nil", wc.Params)
	}
	if err := UpdateWorkerParams("proj", "missing", map[string]any{"a": 1}); err == nil {
		t.Fatal("unknown worker should fail")
	}
}

func TestUpdateWorkerMetaLabel(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := CreateProject("gamma", "", fakeTemplate()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := UpdateWorkerMeta("gamma", "niq", "值班机器人", []string{"ops"}, "nightly"); err != nil {
		t.Fatalf("UpdateWorkerMeta: %v", err)
	}
	set, _ := LoadWorkers("gamma")
	wc, ok := FindWorker(set, "niq")
	if !ok || wc.Label != "值班机器人" || wc.Description != "nightly" || len(wc.Tags) != 1 {
		t.Fatalf("declaration = %+v", wc)
	}
	for _, bad := range []string{" padded", "two\nlines", strings.Repeat("x", 65)} {
		if err := UpdateWorkerMeta("gamma", "niq", bad, nil, ""); err == nil {
			t.Fatalf("label %q was accepted", bad)
		}
	}
	set, _ = LoadWorkers("gamma")
	if wc, _ := FindWorker(set, "niq"); wc.Label != "值班机器人" {
		t.Fatalf("rejected edit changed label: %q", wc.Label)
	}
	if err := UpdateWorkerMeta("gamma", "niq", "", nil, ""); err != nil {
		t.Fatalf("clear label: %v", err)
	}
	set, _ = LoadWorkers("gamma")
	if wc, _ := FindWorker(set, "niq"); wc.Label != "" {
		t.Fatalf("label not cleared: %q", wc.Label)
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
	set, err := LoadWorkers("legacy")
	if err != nil {
		t.Fatalf("LoadWorkers: %v", err)
	}
	if wc, ok := FindWorker(set, "niq"); !ok || wc.Params["instruction"] != "do x" {
		t.Fatalf("legacy declaration lost: %+v", wc)
	}
	if _, err := os.Stat(ArchivedPath("legacy")); err != nil {
		t.Fatalf("archived file missing: %v", err)
	}
}
