package provider

import (
	"os"
	"strings"
	"testing"
)

// TestResolveValue: provider.json's api_key and header values accept the same
// references worker params do, and ${VAR} keeps working for compatibility.
func TestResolveValue(t *testing.T) {
	t.Setenv("PROV_TEST_KEY", "from-env")

	// Mirrors the real provider: vault entries and env: references resolve,
	// anything else (including a miss) comes back unchanged.
	resolve := func(ref string) string {
		switch {
		case ref == "vault:openai/main":
			return "from-vault"
		case strings.HasPrefix(ref, "env:"):
			return os.Getenv(strings.TrimPrefix(ref, "env:"))
		default:
			return ref
		}
	}

	cases := []struct {
		in   string
		want string
	}{
		{"vault:openai/main", "from-vault"},
		{"env:PROV_TEST_KEY", "from-env"},
		{"${PROV_TEST_KEY}", "from-env"},
		{"Bearer ${PROV_TEST_KEY}", "Bearer from-env"},
		{"sk-literal", "sk-literal"},
	}
	for _, c := range cases {
		if got := resolveValue(c.in, resolve); got != c.want {
			t.Errorf("resolveValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Without a provider only ${VAR} expansion happens (the old behaviour).
	if got := resolveValue("${PROV_TEST_KEY}", nil); got != "from-env" {
		t.Errorf("nil resolver = %q, want env expansion", got)
	}
	if got := resolveValue("vault:openai/main", nil); got != "vault:openai/main" {
		t.Errorf("nil resolver = %q, want the reference unchanged", got)
	}
}
