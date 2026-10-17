package mcp

import "testing"

func TestParseConfigDefaultExpose(t *testing.T) {
	cfg, err := ParseConfig(map[string]any{})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Expose != ExposeAgentic {
		t.Fatalf("default expose = %q, want %q", cfg.Expose, ExposeAgentic)
	}
}

func TestParseConfigFlatServersAndHeadersEnv(t *testing.T) {
	t.Setenv("MCP_TOKEN", "sekret")
	cfg, err := ParseConfig(map[string]any{
		"expose": "ready",
		"servers": []any{
			map[string]any{
				"name":    "files",
				"url":     "https://mcp.example/files",
				"headers": map[string]any{"Authorization": "Bearer ${MCP_TOKEN}"},
			},
		},
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Expose != ExposeReady {
		t.Fatalf("expose = %q, want ready", cfg.Expose)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(cfg.Servers))
	}
	s := cfg.Servers[0]
	if s.Name != "files" || s.URL != "https://mcp.example/files" {
		t.Fatalf("server = %+v", s)
	}
	if got := s.Headers["Authorization"]; got != "Bearer sekret" {
		t.Fatalf("authorization header = %q, want resolved ${ENV}", got)
	}
}

func TestParseConfigFixed(t *testing.T) {
	cfg, err := ParseConfig(map[string]any{"fixed": true})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !cfg.Fixed {
		t.Fatal("fixed not parsed")
	}
	def, err := ParseConfig(map[string]any{})
	if err != nil {
		t.Fatalf("ParseConfig default: %v", err)
	}
	if def.Fixed {
		t.Fatal("fixed must default to false (dynamic)")
	}
}

// TestParseConfigLegacyNested keeps existing params.mcp projects working, with
// the flat keys winning on a conflict.
func TestParseConfigLegacyNested(t *testing.T) {
	cfg, err := ParseConfig(map[string]any{
		"mcp":    map[string]any{"expose": "ready", "fixed": true},
		"expose": "agentic",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Expose != ExposeAgentic {
		t.Fatalf("flat expose should win, got %q", cfg.Expose)
	}
	if !cfg.Fixed {
		t.Fatal("nested fixed should still be read")
	}
}

func TestParseConfigValidation(t *testing.T) {
	if _, err := ParseConfig(map[string]any{
		"servers": []any{map[string]any{"name": "no-url"}},
	}); err == nil {
		t.Fatal("expected error for server missing url")
	}
	if _, err := ParseConfig(map[string]any{"expose": "nope"}); err == nil {
		t.Fatal("expected error for unknown expose mode")
	}
	if _, err := ParseConfig(map[string]any{"mcp": "nope"}); err == nil {
		t.Fatal("expected error for non-object params.mcp")
	}
}
