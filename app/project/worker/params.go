// Creation parameters: what the flat params map carries and what each worker
// type is allowed to put in it.
//
// A declaration's params are the ONLY construction input a builder reads. This
// file owns the per-type accepted-key contract and the small readers (mounts,
// programs, events) the builders use. For a managed-process worker the same
// params become the child's environment; validated in backend.go.
package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/niq-run/niq/core/impl/workers/reason"
	"github.com/niq-run/niq/core/itfs/event"
	programpkg "github.com/niq-run/niq/core/itfs/program"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// Params returns the declaration's construction params. It is the single
// construction bag; no typed field overlays it.
func Params(d Declaration) map[string]any { return d.Params }

// SpawnConfig lowers a declaration into the lifecycle-level config the worker
// service builds from. The declaration's ACL travels with it so the builder can
// register the identity without a second store lookup.
func SpawnConfig(d Declaration) coreworker.WorkerConfig {
	return coreworker.WorkerConfig{
		ID:             d.ID,
		Type:           d.Type,
		Params:         d.Params,
		Label:          d.Label,
		Tags:           append([]string(nil), d.Tags...),
		Description:    d.Description,
		PublishAllow:   publishPatterns(d.Publish),
		SubscribeAllow: subscriptionPatterns(d.Subscriptions),
	}
}

// Spec lowers a declaration into a full lifecycle spec, including the launch
// fields a managed-process worker needs. Env is derived from Params by the
// backend, not stored here.
func Spec(d Declaration) coreworker.WorkerSpec {
	return coreworker.WorkerSpec{
		WorkerConfig: SpawnConfig(d),
		Placement:    PlacementOf(d),
		Command:      d.Command,
		Cwd:          d.Cwd,
	}
}

// DeclFromSpec turns a lifecycle spec into a declaration. The params map is
// stored verbatim. ACL patterns are lowered back to their config spelling;
// display metadata is carried on typed fields.
func DeclFromSpec(spec coreworker.WorkerSpec) Declaration {
	placement := spec.Placement
	if placement == "" {
		placement = coreworker.PlacementInProcess
	}
	return Declaration{
		Config: Config{
			ID:     spec.ID,
			Type:   spec.Type,
			Params: spec.Params,
		},
		Placement:       placement,
		Label:           spec.Label,
		Tags:            append([]string(nil), spec.Tags...),
		Description:     spec.Description,
		Publish:         publishSpecs(spec.PublishAllow),
		Subscriptions:   subscriptionSpecs(spec.SubscribeAllow),
		Command:         spec.Command,
		Cwd:             spec.Cwd,
		TokenTTLSeconds: spec.TokenTTLSeconds,
	}
}

// publishPatterns lowers config publish entries to bus patterns.
func publishPatterns(specs []PublishSpec) []event.PublishPattern {
	if len(specs) == 0 {
		return nil
	}
	out := make([]event.PublishPattern, 0, len(specs))
	for _, p := range specs {
		out = append(out, p.ToPublishPattern())
	}
	return out
}

// subscriptionPatterns lowers config subscribe entries to bus patterns.
func subscriptionPatterns(specs []SubscriptionSpec) []event.EventPattern {
	if len(specs) == 0 {
		return nil
	}
	out := make([]event.EventPattern, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.ToPattern())
	}
	return out
}

// publishSpecs is the inverse of publishPatterns.
func publishSpecs(patterns []event.PublishPattern) []PublishSpec {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]PublishSpec, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, PublishSpec{Type: p.Type, Target: p.Target})
	}
	return out
}

// subscriptionSpecs is the inverse of subscriptionPatterns.
func subscriptionSpecs(patterns []event.EventPattern) []SubscriptionSpec {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]SubscriptionSpec, 0, len(patterns))
	for _, s := range patterns {
		out = append(out, SubscriptionSpec{Type: s.Type, Source: s.SourceID})
	}
	return out
}

// parseMountsParam extracts the mount list from worker params: "mounts" as a
// []string (config) or []any of strings (bus payloads), or the single-mount
// "path" sugar used by the host spawn tool. Each path is ~-expanded and made
// absolute. An absent parameter yields a nil slice.
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
func ValidateProcessParams(params map[string]any) error {
	for k, v := range params {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("params key %q is not a valid environment variable name", k)
		}
		if strings.HasPrefix(k, "NIQ_") {
			return fmt.Errorf("params key %q is reserved by the host", k)
		}
		if _, ok := v.(string); !ok {
			return fmt.Errorf("params value for %q must be a string (managed-process params become env)", k)
		}
	}
	return nil
}

// ── spawn-parameter contract ──

// SpawnParamsByType is the per-type spawn contract — the keys that type's
// builder (and the underlying worker) actually read. Keep it in step with the
// builders in this package: TestSpawnParamsCoverEveryBuilder fails when a
// builder type has no entry here.
var SpawnParamsByType = map[string][]string{
	"reason": {
		"instruction", "goal", "brief", "programs", "events", "readme",
		"provider", "model", "collaborators",
		"budget_soft", "budget_hard",
		"tail_tokens", "keep_tail", "compact_directive",
	},
	"coding": {
		"instruction", "goal", "brief", "programs", "events", "readme",
		"provider", "model", "collaborators",
		"budget_soft", "budget_hard",
		"tail_tokens", "keep_tail", "compact_directive",
		"tool_mode",
	},
	"workspace": {"mounts", "path", "approver"},
	"program":   {"mounts", "path"},
	"host":      {"readme", "readme_append"},
	"timer":     {"readme", "readme_append"},
	"directory": {"readme", "readme_append"},
	"history":   {"max_list_events", "max_list_item_bytes", "readme", "readme_append"},
	"mcp":       {"expose", "fixed", "servers", "allowed_tools", "mcp", "readme", "readme_append"},
	"niq":       {"remote_url", "remote_worker_id", "remote_credential", "recipient", "remote_recipient"},
}

// spawnParamAliases names the mistakes worth pointing at. Only applied when the
// suggested key is actually valid for the type being spawned, so a workspace
// spawn with "prompt" gets the plain unknown-parameter error instead of a
// misleading pointer.
var spawnParamAliases = map[string]string{
	"prompt":        "instruction",
	"system_prompt": "instruction",
	"sysprompt":     "instruction",
}

// CheckSpawnParams returns the validator for one type: it reports every
// parameter the type does not read, and lists what it does.
func CheckSpawnParams(typ string, typeParams []string) func(params map[string]any) error {
	accepted := append([]string(nil), typeParams...)
	sort.Strings(accepted)

	valid := make(map[string]bool, len(accepted))
	for _, k := range accepted {
		valid[k] = true
	}
	acceptedList := strings.Join(accepted, ", ")

	return func(params map[string]any) error {
		var unknown []string
		for k := range params {
			if !valid[k] {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) == 0 {
			return nil
		}
		sort.Strings(unknown)

		described := make([]string, 0, len(unknown))
		for _, k := range unknown {
			if alias, ok := spawnParamAliases[k]; ok && valid[alias] {
				described = append(described, fmt.Sprintf("%q (did you mean %q?)", k, alias))
				continue
			}
			described = append(described, fmt.Sprintf("%q", k))
		}
		noun := "parameter"
		if len(unknown) > 1 {
			noun = "parameters"
		}
		return fmt.Errorf("unknown spawn %s %s; %s accepts: %s",
			noun, strings.Join(described, ", "), typ, acceptedList)
	}
}

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
