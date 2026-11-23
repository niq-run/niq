// Package worker owns the worker model of a project: what a worker declaration
// looks like, how its params are projected and validated, which spawn
// parameters each type accepts, where the worker runs (its placement), how a
// type is constructed, how third-party workers are provisioned and supervised,
// and how its lifecycle is driven.
//
// It deliberately does NOT know about the project file layout: manifests are
// read and written through the ManifestStore interface, which
// app/standalone/store implements over workers.json / workers.archived.json.
// The dependency stays one-way — store imports this package's value types,
// never the reverse — which is also what lets the whole lifecycle be tested
// over an in-memory store.
package worker

import (
	"fmt"
	"strings"

	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// Definition is the worker's construction configuration: its identity and the
// params bag. Everything a builder or a supervised process consumes is here.
type Definition struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// Manifest is one worker's persisted declaration (workers.json or
// workers.archived.json): its definition and how it is launched. It is
// Definition plus the fields that are deliberately NOT construction params:
// placement, display metadata, and the local-process launch spec.
//
// Bus identity — the publish/subscribe grants and the credential — is NOT here.
// It belongs to the bus control plane (core/itfs/bus.Control), which owns the
// durable identity record; the manifest names the worker, the identity says what
// it may do.
type Manifest struct {
	Definition

	// Placement says where the worker runs and who owns its lifecycle.
	Placement coreworker.Placement `json:"placement,omitempty"`

	// Display metadata. Not handed to builders.
	Label       string   `json:"label,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`

	// Local third-party process launch spec.
	Command []string `json:"command,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
}

// Placement is the core placement type, re-exported for the project layer.
type Placement = coreworker.Placement

// Placement spellings, re-exported so callers do not need the core worker
// package just to name one.
const (
	PlacementInProcess        = coreworker.PlacementInProcess
	PlacementManagedProcess   = coreworker.PlacementManagedProcess
	PlacementUnmanagedProcess = coreworker.PlacementUnmanagedProcess
)

// PlacementOf resolves a declaration's placement, defaulting to in-process.
func PlacementOf(d Manifest) coreworker.Placement {
	if d.Placement == "" {
		return coreworker.PlacementInProcess
	}
	return d.Placement
}

// IsInProcess reports whether a declaration builds a worker in the host
// process.
func IsInProcess(d Manifest) bool {
	return PlacementOf(d) == coreworker.PlacementInProcess
}

// ProcessDeclarations returns the declarations whose worker runs as its own OS
// process (managed-process or unmanaged-process).
func ProcessDeclarations(ds []Manifest) []Manifest {
	var out []Manifest
	for _, d := range ds {
		if !IsInProcess(d) {
			out = append(out, d)
		}
	}
	return out
}

// ValidateMeta checks a worker's display metadata (label / tags / description).
// Tags form a slash-path hierarchy, so each segment may carry letters, digits
// and '_-.'; the slash is the path separator. Segments must be non-empty (no
// "//", no leading/trailing slash). Description is free text but trimmed;
// empty values are dropped.
func ValidateMeta(d Manifest) error {
	// Label is a display name shown in place of the id, so it only needs to
	// stay one short, printable line — the id remains the identity.
	if label := strings.TrimSpace(d.Label); label != d.Label {
		return fmt.Errorf("label may not have leading or trailing whitespace")
	}
	if n := len([]rune(d.Label)); n > 64 {
		return fmt.Errorf("label is %d characters, at most 64 are allowed", n)
	}
	for _, r := range d.Label {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("label may not contain control characters")
		}
	}
	for _, t := range d.Tags {
		ts := strings.TrimSpace(t)
		if ts == "" {
			return fmt.Errorf("tags may not be empty")
		}
		for _, seg := range strings.Split(ts, "/") {
			if seg == "" {
				return fmt.Errorf("tag %q: empty segment (no leading/trailing/double slash)", ts)
			}
			for _, r := range seg {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
					return fmt.Errorf("tag %q: segments may only contain letters, digits, '-', '_', '.'", ts)
				}
			}
		}
	}
	return nil
}

// SanitizeID makes a string safe as a worker id / directory name: spaces to
// underscores, slashes to dashes, no leading slash.
func SanitizeID(path string) string {
	s := strings.TrimPrefix(path, "/")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

// Instantiate resolves template-layer placeholders in a declaration for a
// concrete project: "{project}" in any params value becomes the project id.
// Called once when a declaration is instantiated into a project (template
// seeding, WebUI creation) — the persisted declaration carries real paths and
// the runtime never sees the placeholder.
func Instantiate(d Manifest, projectID string) Manifest {
	d.Params = instantiateValue(d.Params, projectID).(map[string]any)
	return d
}

// InstantiateForm resolves the same "{project}" placeholders in a raw create-form
// body (the WebUI's JSON) — params AND launch fields alike — so a form and a
// template-declared worker are instantiated the same way.
func InstantiateForm(form map[string]any, projectID string) map[string]any {
	out, ok := instantiateValue(form, projectID).(map[string]any)
	if !ok {
		return form
	}
	return out
}

// instantiateValue walks a params value recursively, replacing {project} in
// strings and rebuilding slices/maps. It returns nil for nil input.
func instantiateValue(v any, projectID string) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, "{project}", projectID)
	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = strings.ReplaceAll(s, "{project}", projectID)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = instantiateValue(item, projectID)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = instantiateValue(item, projectID)
		}
		return out
	default:
		return v
	}
}
