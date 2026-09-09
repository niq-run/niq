// Project → template export: build a TemplateConfig from a project's declared
// workers, so a project can serve as a clone source just like another template.
// Declarations carry their config (typed fields and/or a raw params map); a
// worker's runtime state stays behind. Secrets (the reason worker's api_key, an
// unmanaged worker's bus credential) never enter the template, and params the
// template schema cannot express are dropped.
package project

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/niq-run/niq/core/worker"
)

// ExportProjectTemplate builds a template from a project's worker
// declarations. Workers archived in project.json are skipped — a template is a
// starting point, not an archive. An empty result (no workers left after the
// skip) is an error rather than a hollow template file. With withProgram, a
// reason worker's programs param is carried as the Programs field; the
// programs/ resources are copied separately (ExportProjectPrograms).
func ExportProjectTemplate(projectID string, withProgram bool) (*TemplateConfig, error) {
	p, err := LoadProject(projectID)
	if err != nil {
		return nil, err
	}
	var workers []WorkerConfig
	for _, w := range p.Workers {
		if p.Archived[w.ID] {
			continue
		}
		if isManagedWorker(w) {
			cfg := spawnConfig(w)
			wc, err := workerConfigFromParams(cfg, withProgram)
			if err != nil {
				return nil, fmt.Errorf("project: worker %s: %w", w.ID, err)
			}
			workers = append(workers, wc)
			continue
		}
		// Unmanaged: the declaration is already the launch spec; only the
		// per-project bus credential stays behind.
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

// ProjectProgramsDir returns a project's programs resources directory.
func ProjectProgramsDir(id string) string {
	return filepath.Join(ProjectDir(id), "programs")
}

// ExportProjectPrograms copies a project's programs resources into a template
// directory's programs/ subdirectory. A project without a programs dir is a
// no-op (false, nil).
func ExportProjectPrograms(projectID, tmplDir string) (bool, error) {
	src := ProjectProgramsDir(projectID)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := CopyDir(src, filepath.Join(tmplDir, "programs")); err != nil {
		return false, err
	}
	return true, nil
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
	Programs      []ProgramSpec      `json:"programs"`
}

// exportableParams is what paramsShape can carry; anything else in a worker's
// persisted params (goal/brief/programs/context tuning — spawn-tool extras)
// has no template field and is dropped with a log line.
var exportableParams = map[string]bool{
	"instruction": true, "provider": true, "api_key": true, "base_url": true,
	"model": true, "subscriptions": true, "publish": true, "mounts": true,
	"approver": true,
}

// workerConfigFromParams converts a declared worker's effective params back
// into the template WorkerConfig it was seeded from. The params map travels
// through JSON (not hand-parsed) so the subscription/publish entries reuse
// their own unmarshalers. The api_key is deliberately not carried over; with
// withProgram the programs param is lifted into the Programs field.
func workerConfigFromParams(cfg worker.WorkerConfig, withProgram bool) (WorkerConfig, error) {
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
		if !exportableParams[k] && !(withProgram && k == "programs") {
			dropped = append(dropped, k)
		}
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		log.Printf("[project] export worker %s: params without a template field are dropped: %v", cfg.ID, dropped)
	}

	programs := shape.Programs
	if !withProgram {
		programs = nil
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
		Programs:      programs,
	}, nil
}
