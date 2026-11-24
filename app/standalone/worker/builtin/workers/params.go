package workers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/niq-run/niq/core/impl/workers/reason"
	"github.com/niq-run/niq/core/itfs/event"
	programpkg "github.com/niq-run/niq/core/itfs/program"
)

func parseMountsParam(p map[string]any) ([]string, error) {
	var raws []string
	switch v := p["mounts"].(type) {
	case []string:
		raws = v
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("workspace: mounts must be directory paths")
			}
			raws = append(raws, s)
		}
	}
	if len(raws) == 0 {
		if path, _ := p["path"].(string); path != "" {
			raws = []string{path}
		}
	}
	out := make([]string, 0, len(raws))
	for _, raw := range raws {
		expanded := raw
		if expanded == "~" || strings.HasPrefix(expanded, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("workspace: resolve home: %w", err)
			}
			if expanded == "~" {
				expanded = home
			} else {
				expanded = filepath.Join(home, expanded[2:])
			}
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return nil, fmt.Errorf("workspace: bad mount %q: %w", raw, err)
		}
		out = append(out, abs)
	}
	return out, nil
}

// ValidateProcessParams checks that a managed-process worker's params can be
// handed to the child as environment variables: a flat map of valid env names
// to strings. The host-owned NIQ_* variables are reserved.

// ── param readers ──

// ParsePrograms extracts a simplified program list from spawn params. It is the
// inverse of the typed Programs projection the builders consume.
func ParsePrograms(p map[string]any, workerID string) []programpkg.Program {
	raw, ok := p["programs"].([]any)
	if !ok || len(raw) == 0 {
		if instr, _ := p["instruction"].(string); instr != "" {
			return []programpkg.Program{
				{
					Meta: programpkg.Meta{
						Name:        workerID + "-instruction",
						ContentType: programpkg.ContentTypeInstruction,
					},
					EntryContent: programpkg.ProgramContent{Content: instr},
				},
			}
		}
		return []programpkg.Program{
			{
				Meta: programpkg.Meta{
					Name:        workerID + "-instruction",
					ContentType: programpkg.ContentTypeInstruction,
				},
			},
		}
	}

	progs := make([]programpkg.Program, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		ctStr, _ := m["content_type"].(string)
		if name == "" || ctStr == "" {
			continue
		}
		var ct programpkg.ContentType
		switch ctStr {
		case "instruction":
			ct = programpkg.ContentTypeInstruction
		case "playbook":
			ct = programpkg.ContentTypePlaybook
		default:
			continue
		}
		desc, _ := m["description"].(string)
		content, _ := m["content"].(string)
		progs = append(progs, programpkg.Program{
			Meta: programpkg.Meta{
				Name:        name,
				ContentType: ct,
				Description: desc,
			},
			EntryContent: programpkg.ProgramContent{Content: content},
		})
	}
	if len(progs) == 0 {
		progs = append(progs, programpkg.Program{
			Meta: programpkg.Meta{
				Name:        workerID + "-instruction",
				ContentType: programpkg.ContentTypeInstruction,
			},
		})
	}
	return progs
}

// parseEvents extracts event type subscriptions from spawn params.
func parseEvents(p map[string]any) []reason.EventConverter {
	raw, ok := p["events"].([]any)
	if !ok {
		return nil
	}
	handlers := make([]reason.EventConverter, 0, len(raw))
	for _, r := range raw {
		evtType, ok := r.(string)
		if !ok || evtType == "" {
			continue
		}
		handlers = append(handlers, reason.EventConverter{
			Pattern:   event.NewPattern(event.EventType(evtType)),
			Converter: reason.DefaultConverter,
		})
	}
	return handlers
}

// pStrings reads an optional string-list worker param. Accepts a JSON array or
// a comma-separated string. It returns nil when the key is absent ("not
// configured") and a non-nil slice when present, so callers can tell an empty
// whitelist from an unset one.
func pStrings(p map[string]any, key string) []string {
	raw, ok := p[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string{}, v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if v == "" {
			return []string{}
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// pInt reads an optional int worker param (JSON numbers arrive as float64).
func pInt(p map[string]any, key string) int {
	switch n := p[key].(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}
