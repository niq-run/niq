package template

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/app/niqhome"
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
	setupHome(t)
	data := makeZip(t, map[string]string{
		"template.json":            validTmpl,
		"programs/prog/PROGRAM.md": "# prog",
	})
	cfg, err := ImportZip("z1", data)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(cfg.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(cfg.Workers))
	}
	// Programs dir must have landed on disk.
	if _, err := os.Stat(ProgramsDir(TemplatesDir(), "z1")); err != nil {
		t.Fatalf("programs dir: %v", err)
	}
	if _, err := os.Stat(Path(TemplatesDir(), "z1")); err != nil {
		t.Fatalf("template.json missing: %v", err)
	}
}

func TestImportTemplateZipWrapped(t *testing.T) {
	setupHome(t)
	data := makeZip(t, map[string]string{
		"wrapped/template.json": validTmpl,
	})
	cfg, err := ImportZip("z2", data)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(cfg.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(cfg.Workers))
	}
}

func TestImportTemplateZipDerivesNameFromWrapper(t *testing.T) {
	setupHome(t)
	data := makeZip(t, map[string]string{
		"my-shared-template/template.json": validTmpl,
	})
	cfg, err := ImportZip("", data)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(cfg.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(cfg.Workers))
	}
	if _, err := os.Stat(Path(TemplatesDir(), "my-shared-template")); err != nil {
		t.Fatalf("derived name missing: %v", err)
	}
}

func TestImportTemplateZipRejectsNameRequired(t *testing.T) {
	setupHome(t)
	// No wrapping folder and no id -> nothing to key the template on.
	data := makeZip(t, map[string]string{"template.json": validTmpl})
	if _, err := ImportZip("", data); err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestImportTemplateZipRejectsInvalid(t *testing.T) {
	setupHome(t)
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
			if _, err := ImportZip("ztest", makeZip(t, tt.files)); err == nil {
				t.Fatal("expected import to fail")
			}
		})
	}
}

func TestImportTemplateZipRejectsDuplicate(t *testing.T) {
	setupHome(t)
	if _, err := ImportZip("dup", makeZip(t, map[string]string{"template.json": validTmpl})); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportZip("dup", makeZip(t, map[string]string{"template.json": validTmpl})); err == nil {
		t.Fatal("expected duplicate to fail")
	}
}

// TestImportTemplateZipSlipGuard asserts an entry cannot escape the destination
// directory through path traversal.
func TestImportTemplateZipSlipGuard(t *testing.T) {
	setupHome(t)
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
	if _, err := ImportZip("safe", buf.Bytes()); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := os.Stat(filepath.Join(TemplatesDir(), "..", "escape.txt")); err == nil {
		t.Fatal("traversal file escaped the templates dir")
	}
}

// setupHome isolates the template root (and the niq root it derives from) under
// a temp HOME, so tests never touch a real ~/.niq.
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.Unsetenv(niqhome.Env)
	return filepath.Join(home, ".niq")
}
