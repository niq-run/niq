// Project → template export: build a TemplateConfig from a project's on-disk
// worker definitions, so a project can serve as a clone source just like
// another template. Managed workers come from their authoritative config.json,
// unmanaged workers from their project.json launch spec. Secrets (the reason
// worker's api_key, an unmanaged worker's bus credential) never enter the
// template, and params the template schema cannot express are dropped.
package project

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"

	"github.com/niq-run/niq/core/worker"
)

// ExportProjectTemplate builds a template from a project's worker
// declarations. Workers archived in project.json are skipped — a template is a
// starting point, not an archive. An empty result (no workers left after the
// skip) is an error rather than a hollow template file.
func ExportProjectTemplate(projectID string) (*TemplateConfig, error) {
	p, err := LoadProject(projectID)
	if err != nil {
		return nil, err
	}
	var workers []WorkerConfig
	for _, w := range p.Workers {
		if p.Archived[w.ID] {
			continue
		}
		if w.Managed {
			cfg, ok := readWorkerConfig(ProjectDir(projectID), w.ID)
			if !ok {
				return nil, fmt.Errorf("project: worker %s: no config.json", w.ID)
			}
			wc, err := workerConfigFromParams(cfg)
			if err != nil {
				return nil, fmt.Errorf("project: worker %s: %w", w.ID, err)
			}
			workers = append(workers, wc)
			continue
		}
		// Unmanaged: the project.json declaration is already the launch spec;
		// only the per-project bus credential stays behind.
		managed := false
		workers = append(workers, WorkerConfig{
			Type:          w.Type,
			ID:            w.ID,
			Managed:       &managed,
			Command:       w.Command,
			Env:           w.Env,
			Cwd:           w.Cwd,
			Subscriptions: w.Subscriptions,
			Publish:       w.Publish,
		})
	}
	if len(workers) == 0 {
		return nil, fmt.Errorf("project: %q has no exportable workers", projectID)
	}
	return validateWorkers(&TemplateConfig{Workers: workers})
}

// paramsShape mirrors the params keys workerConfigParams writes, with
// subscription/publish entries decoded through their flexible (string-or-
// object) unmarshalers. It is the inverse path of workerConfigParams; a
// round-trip template → project → template preserves every field it covers.
type paramsShape struct {
	Instruction   string             `json:"instruction"`
	Provider      string             `json:"provider"`
	APIKey        string             `json:"api_key"`
	BaseURL       string             `json:"base_url"`
	Model         string             `json:"model"`
	Subscriptions []SubscriptionSpec `json:"subscriptions"`
	Publish       []PublishSpec      `json:"publish"`
	Mounts        []string           `json:"mounts"`
	Approver      string             `json:"approver"`
}

// exportableParams is what paramsShape can carry; anything else in a worker's
// persisted params (goal/brief/programs/context tuning — spawn-tool extras)
// has no template field and is dropped with a log line.
var exportableParams = map[string]bool{
	"instruction": true, "provider": true, "api_key": true, "base_url": true,
	"model": true, "subscriptions": true, "publish": true, "mounts": true,
	"approver": true,
}

// workerConfigFromParams converts a persisted worker config back into the
// template WorkerConfig it was seeded from. The params map travels through
// JSON (not hand-parsed) so the subscription/publish entries reuse their own
// unmarshalers. The api_key is deliberately not carried over.
func workerConfigFromParams(cfg worker.WorkerConfig) (WorkerConfig, error) {
	raw, err := json.Marshal(cfg.Params)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf("encode params: %w", err)
	}
	var shape paramsShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		return WorkerConfig{}, fmt.Errorf("decode params: %w", err)
	}

	var dropped []string
	for k := range cfg.Params {
		if !exportableParams[k] {
			dropped = append(dropped, k)
		}
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		log.Printf("[project] export worker %s: params without a template field are dropped: %v", cfg.ID, dropped)
	}

	return WorkerConfig{
		Type:          cfg.Type,
		ID:            cfg.ID,
		Instruction:   shape.Instruction,
		Provider:      shape.Provider,
		BaseURL:       shape.BaseURL,
		Model:         shape.Model,
		Subscriptions: shape.Subscriptions,
		Publish:       shape.Publish,
		Mounts:        shape.Mounts,
		Approver:      shape.Approver,
	}, nil
}
