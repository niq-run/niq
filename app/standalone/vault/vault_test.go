package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPutGetListDelete(t *testing.T) {
	v := New(t.TempDir())

	if err := v.Put("openai/main", "sk-123"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := v.Get("openai/main")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk-123" {
		t.Errorf("Get = %q", got)
	}
	if !v.Has("openai/main") {
		t.Error("Has = false")
	}

	path := filepath.Join(v.Dir(), "openai", "main")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("entry mode = %o, want 600", perm)
	}

	list, err := v.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "openai/main" || list[0].Size == 0 {
		t.Fatalf("List = %+v", list)
	}

	if err := v.Delete("openai/main"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := v.Get("openai/main"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := v.Delete("openai/main"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete twice = %v, want ErrNotFound", err)
	}
}

func TestListMissingRootIsEmpty(t *testing.T) {
	v := New(filepath.Join(t.TempDir(), "never-created"))
	list, err := v.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List = %+v, want empty", list)
	}
}

func TestPutOverwrites(t *testing.T) {
	v := New(t.TempDir())
	if err := v.Put("k", "one"); err != nil {
		t.Fatal(err)
	}
	if err := v.Put("k", "two"); err != nil {
		t.Fatal(err)
	}
	got, _ := v.Get("k")
	if got != "two" {
		t.Errorf("Get = %q, want the overwrite", got)
	}
	list, _ := v.List()
	if len(list) != 1 {
		t.Errorf("List = %+v, want one entry", list)
	}
}

func TestValidateNameRejectsTraversal(t *testing.T) {
	for _, name := range []string{"", "/abs", "trailing/", "../x", "a/../b", "a//b", ".", "a/./b"} {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", name)
		}
	}
	for _, name := range []string{"k", "openai/main", "workers/w1/remote_credential", "a-b_c.d"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v", name, err)
		}
	}
}

func TestResolveChainsVaultEnvFileLiteral(t *testing.T) {
	t.Setenv("VAULT_TEST_ENV", "from-env")
	dir := t.TempDir()
	file := filepath.Join(dir, "tok")
	if err := os.WriteFile(file, []byte(" from-file \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	v := New(filepath.Join(dir, "store"))
	if err := v.Put("mine", "from-vault"); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"vault:mine":         "from-vault",
		"env:VAULT_TEST_ENV": "from-env",
		"file:" + file:       "from-file",
		"literal-token":      "literal-token",
	}
	for ref, want := range cases {
		if got := v.Resolve(ref); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", ref, got, want)
		}
	}

	// A missing entry falls through to the reference, as env/file always have.
	if got := v.Resolve("vault:missing"); got != "vault:missing" {
		t.Errorf("Resolve(missing) = %q", got)
	}
}

func TestStoreRoutesByScheme(t *testing.T) {
	dir := t.TempDir()
	v := New(filepath.Join(dir, "store"))

	if err := v.Store("vault:a/b", "secret"); err != nil {
		t.Fatalf("Store vault: %v", err)
	}
	if got, _ := v.Get("a/b"); got != "secret" {
		t.Errorf("Get = %q", got)
	}

	file := filepath.Join(dir, "f")
	if err := v.Store("file:"+file, "filesecret"); err != nil {
		t.Fatalf("Store file: %v", err)
	}
	if got := v.Resolve("file:" + file); got != "filesecret" {
		t.Errorf("file round trip = %q", got)
	}

	if err := v.Store("env:NOPE", "x"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("Store env: = %v, want read-only", err)
	}
}

func TestIsRef(t *testing.T) {
	for _, s := range []string{"vault:x", "env:X", "file:/p"} {
		if !IsRef(s) {
			t.Errorf("IsRef(%q) = false", s)
		}
	}
	for _, s := range []string{"sk-literal", "", "Vault:x", "my-env"} {
		if IsRef(s) {
			t.Errorf("IsRef(%q) = true", s)
		}
	}
}
