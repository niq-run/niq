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

// ParamSpec is one spawn parameter a type accepts. Secret marks a param that
// carries a credential: the control plane seals a literal value into the vault
// and stores only a `vault:` reference, so the token never lands in
// workers.json.
//
// The contract is a single declaration — accepted keys and their secrecy — so a
// new credential param cannot be added without its secrecy being decided in the
// same edit.
type ParamSpec struct {
	Name   string
	Secret bool
}

// Names returns the accepted parameter names of a contract, in declaration order.
func Names(specs []ParamSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// SecretNames returns the names of the credential-bearing params, in declaration
// order. It is empty when the type has none.
func SecretNames(specs []ParamSpec) []string {
	var out []string
	for _, s := range specs {
		if s.Secret {
			out = append(out, s.Name)
		}
	}
	return out
}

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

// CheckSpawnParams returns the validator for one type: it reports every
// parameter the type does not read, and lists what it does.
func CheckSpawnParams(typ string, specs []ParamSpec) func(params map[string]any) error {
	accepted := Names(specs)
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
