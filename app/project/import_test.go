package project

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// makeZip builds a zip archive in memory from a map of name -> content.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const validTmpl = `{"workers":[{"type":"reason","id":"niq"},{"type":"program","id":"program"}]}`

func TestImportTemplateZipAtRoot(t *testing.T) {
	setupProjectsRoot(t)
	data := makeZip(t, map[string]string{
		"template.json":            validTmpl,
		"programs/prog/PROGRAM.md": "# prog",
	})
	cfg, err := ImportTemplateZip("z1", data)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(cfg.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(cfg.Workers))
	}
	// Programs dir must have landed on disk.
	if _, err := os.Stat(TemplateProgramsDir(TemplatesDir(), "z1")); err != nil {
		t.Fatalf("programs dir: %v", err)
	}
	if _, err := os.Stat(TemplatePath(TemplatesDir(), "z1")); err != nil {
		t.Fatalf("template.json missing: %v", err)
	}
}

func TestImportTemplateZipWrapped(t *testing.T) {
	setupProjectsRoot(t)
	data := makeZip(t, map[string]string{
		"wrapped/template.json": validTmpl,
	})
	cfg, err := ImportTemplateZip("z2", data)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(cfg.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(cfg.Workers))
	}
}

func TestImportTemplateZipDerivesNameFromWrapper(t *testing.T) {
	setupProjectsRoot(t)
	data := makeZip(t, map[string]string{
		"my-shared-template/template.json": validTmpl,
	})
	cfg, err := ImportTemplateZip("", data)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(cfg.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(cfg.Workers))
	}
	if _, err := os.Stat(TemplatePath(TemplatesDir(), "my-shared-template")); err != nil {
		t.Fatalf("derived name missing: %v", err)
	}
}

func TestImportTemplateZipRejectsNameRequired(t *testing.T) {
	setupProjectsRoot(t)
	// No wrapping folder and no id -> nothing to key the template on.
	data := makeZip(t, map[string]string{"template.json": validTmpl})
	if _, err := ImportTemplateZip("", data); err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestImportTemplateZipRejectsInvalid(t *testing.T) {
	setupProjectsRoot(t)
	tests := []struct {
		name  string
		files map[string]string
	}{
		{"no template.json", map[string]string{"programs/p.md": "x"}},
		{"invalid json", map[string]string{"template.json": `{bad`}},
		{"empty workers", map[string]string{"template.json": `{"workers":[]}`}},
		{"stray root file", map[string]string{"template.json": validTmpl, "README.md": "hi"}},
		{"mixed wrapper", map[string]string{"a/template.json": validTmpl, "b/x.md": "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ImportTemplateZip("ztest", makeZip(t, tt.files)); err == nil {
				t.Fatal("expected import to fail")
			}
		})
	}
}

func TestImportTemplateZipRejectsDuplicate(t *testing.T) {
	setupProjectsRoot(t)
	if _, err := ImportTemplateZip("dup", makeZip(t, map[string]string{"template.json": validTmpl})); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportTemplateZip("dup", makeZip(t, map[string]string{"template.json": validTmpl})); err == nil {
		t.Fatal("expected duplicate to fail")
	}
}

// TestImportTemplateZipSlipGuard asserts an entry cannot escape the destination
// directory through path traversal.
func TestImportTemplateZipSlipGuard(t *testing.T) {
	setupProjectsRoot(t)
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("template.json")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte(validTmpl))
	evil, _ := w.Create("../../escape.txt")
	evil.Write([]byte("x"))
	w.Close()
	if _, err := ImportTemplateZip("safe", buf.Bytes()); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := os.Stat(filepath.Join(TemplatesDir(), "..", "escape.txt")); err == nil {
		t.Fatal("traversal file escaped the templates dir")
	}
}
