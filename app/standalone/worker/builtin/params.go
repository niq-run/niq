package builtin

import (
	"fmt"
	"sort"
	"strings"
)

// ── spawn-parameter contract ──
//
// Each type declares the keys it reads in its own file, next to its builder;
// Registry.RegisterType turns that list into the check. TestSpawnParamsCoverEveryBuilder
// fails when a builder type has no contract, or a contract has no builder.

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
