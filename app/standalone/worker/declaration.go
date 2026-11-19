// Package worker owns the worker model of a project: what a worker declaration
// looks like, how its params are projected and validated, which spawn
// parameters each type accepts, where the worker runs (its placement), how a
// type is constructed, how third-party workers are provisioned and supervised,
// and how its lifecycle is driven.
//
// It deliberately does NOT know about the project file layout: declarations are read and
// written through the DeclStore interface, which the project package implements
// over its on-disk worker files. That keeps the dependency one-way —
// app/project → app/project/worker — with no import cycle, and lets the whole
// lifecycle be tested over an in-memory store.
package worker

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// SubscriptionSpec is one SubscribeAllow entry. It accepts either a bare
// event-type string ("worker.discover") or an object restricting the source:
// {"type": "request.completed", "source": "timer"}. The source field limits
// broadcast delivery to events published by that worker; empty means any
// source. It is the config-level spelling of event.EventPattern.
type SubscriptionSpec struct {
	Type   event.EventType `json:"type"`
	Source string          `json:"source,omitempty"`
}

// ToPattern converts the spec into the bus-level EventPattern it stands for.
// An omitted source is the canonical "any source" wildcard.
func (s SubscriptionSpec) ToPattern() event.EventPattern {
	src := s.Source
	if src == "" {
		src = "*"
	}
	return event.EventPattern{Type: s.Type, SourceID: src}
}

// UnmarshalJSON accepts a bare type string or a {"type","source"} object, so
// existing string-only configs keep parsing.
func (s *SubscriptionSpec) UnmarshalJSON(b []byte) error {
	var typeOnly string
	if err := json.Unmarshal(b, &typeOnly); err == nil {
		s.Type = event.EventType(typeOnly)
		s.Source = ""
		return nil
	}
	type alias SubscriptionSpec
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("project: bad subscription entry %s: %w", b, err)
	}
	*s = SubscriptionSpec(a)
	return nil
}

// PublishSpec is one PublishAllow entry. It accepts either a bare event-type
// string ("request.completed") or an object adding a target restriction:
// {"type": "request.completed", "target": "ws-1"}. The target field limits
// directed sends to that target worker; use "*" (or a bare type string) for
// "any target", which also permits broadcasting the type. It is the
// config-level spelling of event.PublishPattern.
type PublishSpec struct {
	Type   event.EventType `json:"type"`
	Target string          `json:"target,omitempty"`
}

// ToPublishPattern converts the spec into the bus-level PublishPattern it
// stands for.
func (p PublishSpec) ToPublishPattern() event.PublishPattern {
	return event.PublishPattern{Type: p.Type, Target: p.Target}
}

// UnmarshalJSON accepts a bare type string or a {"type","target"} object, so
// existing string-only configs keep parsing. A bare string is an unrestricted
// grant (target "*").
func (p *PublishSpec) UnmarshalJSON(b []byte) error {
	var typeOnly string
	if err := json.Unmarshal(b, &typeOnly); err == nil {
		p.Type = event.EventType(typeOnly)
		p.Target = "*"
		return nil
	}
	type alias PublishSpec
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("project: bad publish entry %s: %w", b, err)
	}
	*p = PublishSpec(a)
	return nil
}

// Config is the worker's construction configuration: its identity and the
// params bag. Everything a builder or a supervised process consumes is here.
type Config struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// Declaration is one worker's persisted declaration (workers.json or
// workers.archived.json). It is Config plus the fields that are deliberately
// NOT construction params: placement, display metadata, bus identity/ACL, and
// local-process launch spec.
type Declaration struct {
	Config

	// Placement says where the worker runs and who owns its lifecycle.
	Placement coreworker.Placement `json:"placement,omitempty"`

	// Display metadata. Not handed to builders.
	Label       string   `json:"label,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`

	// Bus identity / ACL. Persisted in workers.json so the runtime registry can
	// be rebuilt at startup; a local process credential is never persisted.
	Publish         []PublishSpec      `json:"publish,omitempty"`
	Subscriptions   []SubscriptionSpec `json:"subscriptions,omitempty"`
	Credential      string             `json:"credential,omitempty"`
	TokenTTLSeconds *int64             `json:"token_ttl_seconds,omitempty"`

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
func PlacementOf(d Declaration) coreworker.Placement {
	if d.Placement == "" {
		return coreworker.PlacementInProcess
	}
	return d.Placement
}

// IsInProcess reports whether a declaration builds a worker in the host
// process.
func IsInProcess(d Declaration) bool {
	return PlacementOf(d) == coreworker.PlacementInProcess
}

// ProcessDeclarations returns the declarations whose worker runs as its own OS
// process (managed-process or unmanaged-process).
func ProcessDeclarations(ds []Declaration) []Declaration {
	var out []Declaration
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
func ValidateMeta(d Declaration) error {
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
func Instantiate(d Declaration, projectID string) Declaration {
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
