// Package project provides the storage and runtime for niq projects.
//
// A project lives under ~/.niq/projects/<id>/ and owns its own event bus,
// WebUI, worker set and state directories. This package reads project
// templates (JSON), instantiates workers, registers them with WorkerService,
// and manages the project instance lifecycle. This is the only place where
// template files are parsed into worker instances — no new abstractions.
package project

import (
	"encoding/json"
	"fmt"

	"github.com/niq-run/niq/core/event"
)

// TemplateConfig is the top-level structure of a project template file: the
// set of workers a new project is seeded with.
type TemplateConfig struct {
	Workers []WorkerConfig `json:"workers"`

	// UploadDir is where the WebUI stores files uploaded from the talk input
	// (project.json upload_dir). Empty means <project dir>/uploads. Relative
	// paths resolve against the project dir.
	UploadDir string `json:"upload_dir,omitempty"`
}

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
func (s SubscriptionSpec) ToPattern() event.EventPattern {
	return event.EventPattern{Type: s.Type, SourceID: s.Source}
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

// WorkerConfig describes a single worker instance declaration.
type WorkerConfig struct {
	Type          string             `json:"type"` // reason / workspace / host / timer / hiw / program
	ID            string             `json:"id"`
	Instruction   string             `json:"instruction,omitempty"`
	Provider      string             `json:"provider,omitempty"`
	APIKey        string             `json:"api_key,omitempty"`
	BaseURL       string             `json:"base_url,omitempty"`
	Model         string             `json:"model,omitempty"`
	Subscriptions []SubscriptionSpec `json:"subscriptions,omitempty"`
	Publish       []PublishSpec      `json:"publish,omitempty"`
	// Mounts are the directories a workspace/program worker mounts. The
	// first mount is the primary one (relative paths resolve against it).
	Mounts []string `json:"mounts,omitempty"`
	// Approver is the worker boundary-expansion approval requests go to
	// (workspace workers; default webui-hiw, empty disables the flow).
	Approver string `json:"approver,omitempty"`
	Archived bool   `json:"archived,omitempty"`
	// Tags are a slash-path hierarchy used to group workers in the WebUI
	// worker selector, so a large worker swarm stays navigable (e.g.
	// "ops/backup", "service/analytics/etl"). Purely display metadata:
	// never passed to the worker as construction params.
	Tags []string `json:"tags,omitempty"`
	// Description is a short human-readable note explaining what the worker
	// is for. Display metadata only; not a construction param.
	Description string `json:"description,omitempty"`
	// Managed marks the worker as host-managed (in-process, worker dir is the
	// config authority). nil or true = managed; false = an external process
	// launched by the project via Command/Env/Cwd.
	Managed    *bool             `json:"managed,omitempty"`
	Credential string            `json:"credential,omitempty"`
	Command    []string          `json:"command,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	// Params holds the raw type-specific construction params — the wire
	// format every builder reads and the general escape hatch: spawn-time
	// extras (goal/brief/programs/context tuning) and third-party worker
	// types' private keys have no typed field. It overlays the typed fields
	// above: on the same key, Params wins (see workerParams).
	Params map[string]any `json:"params,omitempty"`
	// Programs holds a reason worker's spawn-time program seeds (inline
	// "skills" the worker loads at start). Only carried when an export is
	// asked to include programs.
	Programs []ProgramSpec `json:"programs,omitempty"`
}

// ProgramSpec is one entry of a reason worker's programs param: an inline
// program seed with its content. It is the template-level spelling of the
// params entry build.go's parsePrograms reads.
type ProgramSpec struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"` // instruction | playbook
	Description string `json:"description,omitempty"`
	Content     string `json:"content,omitempty"`
}

func validateWorkers(cfg *TemplateConfig) (*TemplateConfig, error) {
	if len(cfg.Workers) == 0 {
		return nil, fmt.Errorf("project: template needs at least one worker")
	}
	for i, w := range cfg.Workers {
		if w.Type == "" {
			return nil, fmt.Errorf("project: worker %d: type is required", i)
		}
		if w.ID == "" {
			return nil, fmt.Errorf("project: worker %d: id is required", i)
		}
	}
	return cfg, nil
}
