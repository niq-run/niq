// Spawn-parameter validation: what each worker type actually reads.
//
// A spawn carries one flat params map, and every builder picks the keys it
// knows out of it. Without a check, a caller that passes a parameter the type
// does not read ("prompt" for reason, "goal" for workspace) gets a created
// worker and no error — the request looks fulfilled while the intended
// initialisation never happened. The table below is the honest contract: it
// lists, per type, the parameters that type consumes, and CreateWorker rejects
// anything else (see workerhost.WorkerService.RegisterParamCheck).
//
// This is validation at the spawn boundary, not a schema for the declaration
// file: recovery replays declarations that were accepted earlier and must not
// fail because a key has since been retired.
package project

import (
	"fmt"
	"sort"
	"strings"
)

// spawnParamsUniversal are accepted by every managed type. They are not type
// knowledge: identity (the bus payload repeats type/id as flat keys), display
// metadata (the host lifts these out before the builder runs), and the ACL
// grants every declaration can carry. A type that pins its own publish list
// ignores the value, but the WebUI sends each type's defaults as a matter of
// course, so rejecting them would break ordinary creation.
var spawnParamsUniversal = []string{
	"type", "id", "label", "tags", "description", "publish", "subscriptions",
}

// spawnParamsByType is the per-type spawn contract — the keys that type's
// builder (and the underlying worker) actually read. Keep it in step with the
// builders in build.go; TestSpawnParamsCoverEveryBuilder fails when a builder
// type has no entry here.
var spawnParamsByType = map[string][]string{
	// reason family: resolveReasonFamily + parsePrograms/parseEvents. "coding"
	// adds its one extra knob (tool_mode).
	"reason": {
		"instruction", "goal", "brief", "programs", "events", "readme",
		"provider", "model", "collaborators",
		"context_window", "budget_soft", "budget_hard",
		"tail_tokens", "keep_tail", "rotate_rounds", "compact_directive",
	},
	"coding": {
		"instruction", "goal", "brief", "programs", "events", "readme",
		"provider", "model", "collaborators",
		"context_window", "budget_soft", "budget_hard",
		"tail_tokens", "keep_tail", "rotate_rounds", "compact_directive",
		"tool_mode",
	},
	// workspace / program: mounts (or the single-mount "path" sugar) and, for
	// workspace, the approval target. Their readme is built-in, not parameterised.
	"workspace": {"mounts", "path", "approver"},
	"program":   {"mounts", "path"},
	// The lifecycle/infrastructure workers read nothing but their readme
	// overrides (baseworker.RegisterReadme), plus history's cap knobs and the
	// MCP server declaration.
	"host":      {"readme", "readme_append"},
	"timer":     {"readme", "readme_append"},
	"directory": {"readme", "readme_append"},
	"hiw":       {},
	"history":   {"max_list_events", "max_list_item_bytes", "readme", "readme_append"},
	"mcp":       {"expose", "fixed", "servers", "allowed_tools", "mcp", "readme", "readme_append"},
	// niw: the far-side handshake and the two recipient seeds. Its readme is
	// built-in.
	"niw": {"remote_url", "remote_worker_id", "remote_credential", "recipient", "remote_recipient"},
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

// checkSpawnParams returns the validator for one type: it reports every
// parameter the type does not read, and lists what it does, so the caller can
// correct the call instead of re-reading the docs.
func checkSpawnParams(typ string, typeParams []string) func(params map[string]any) error {
	accepted := make([]string, 0, len(spawnParamsUniversal)+len(typeParams))
	accepted = append(accepted, spawnParamsUniversal...)
	accepted = append(accepted, typeParams...)
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
