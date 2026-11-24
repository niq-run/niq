// Package worker owns the worker control plane of one project: the manifest
// model, where a worker runs (its placement), how a type is turned into a
// running worker, and how the lifecycle is driven.
//
// The pieces below it are separate on purpose. app/standalone/worker/builtin
// holds the type table — the built-in types, the spawn parameters each accepts
// and the builder for each — as assembly knowledge rather than mechanism.
// app/standalone/worker/process owns OS children and credential provisioning.
// This package assembles them and knows no type of its own.
//
// It deliberately does NOT know about the project file layout: manifests are
// read and written through the ManifestStore interface, which
// app/standalone/store implements over workers.json / workers.archived.json.
// The dependency stays one-way — store imports this package's value types,
// never the reverse — which is also what lets the lifecycle be tested over an
// in-memory store.
package worker

import (
	"fmt"
	"strings"

	"github.com/niq-run/niq/app/standalone/helper"
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

// SanitizeID makes a string safe as a worker id / directory name. The rule is
// helper.SafeName, which project ids and state directories use too: one id must
// not resolve to two different directories.
func SanitizeID(path string) string { return helper.SafeName(path) }

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

func Params(d Manifest) map[string]any { return d.Params }

// SpawnConfig lowers a declaration into the lifecycle-level config the worker
// manager builds from.
func SpawnConfig(d Manifest) coreworker.WorkerConfig {
	return coreworker.WorkerConfig{
		ID:          d.ID,
		Type:        d.Type,
		Params:      d.Params,
		Label:       d.Label,
		Tags:        append([]string(nil), d.Tags...),
		Description: d.Description,
	}
}

// Spec lowers a declaration into a full lifecycle spec, including the launch
// fields a managed-process worker needs. Env is derived from Params by the
// backend, not stored here.
func Spec(d Manifest) coreworker.WorkerSpec {
	return coreworker.WorkerSpec{
		WorkerConfig: SpawnConfig(d),
		Placement:    PlacementOf(d),
		Command:      d.Command,
		Cwd:          d.Cwd,
	}
}

// ManifestFromSpec turns a lifecycle spec into a manifest. The params map is
// stored verbatim; display metadata is carried on typed fields.
func ManifestFromSpec(spec coreworker.WorkerSpec) Manifest {
	placement := spec.Placement
	if placement == "" {
		placement = coreworker.PlacementInProcess
	}
	return Manifest{
		Definition: Definition{
			ID:     spec.ID,
			Type:   spec.Type,
			Params: spec.Params,
		},
		Placement:   placement,
		Label:       spec.Label,
		Tags:        append([]string(nil), spec.Tags...),
		Description: spec.Description,
		Command:     spec.Command,
		Cwd:         spec.Cwd,
	}
}

// parseMountsParam extracts the mount list from worker params: "mounts" as a
// []string (config) or []any of strings (bus payloads), or the single-mount
// "path" sugar used by the host spawn tool. Each path is ~-expanded and made
// absolute. An absent parameter yields a nil slice.

// ManifestStore is the manifest storage the control plane drives. It is the
// only place that knows WHERE manifests are written; the control plane only
// asks what is declared and records changes.
type ManifestStore interface {
	// Get returns one manifest. ok is false when the id is not declared.
	Get(id string) (*Manifest, bool, error)
	// List returns manifests: archived=false is the active set, archived=true
	// the archived set. Both are returned in declaration order.
	List(archived bool) ([]*Manifest, error)
	// Add records a new manifest. Declaring an existing id is an error: the
	// control plane checks for duplicates so it can report them precisely.
	Add(m *Manifest) error
	// Update replaces an existing manifest; an unknown id is an error.
	Update(m *Manifest) error
	// Delete removes a manifest from either set; an unknown id is a no-op.
	Delete(id string) error
	// Archive moves a manifest between the active and archived files. An
	// archived manifest is kept verbatim (it can be restored unchanged); a
	// missing id in the source file is a no-op.
	Archive(id string, archived bool) error
}
