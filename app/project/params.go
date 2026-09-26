// WorkerConfig ↔ params roundtrip. The typed fields of a WorkerConfig are the
// authored view (template, WebUI form); the params map is what builders read and
// the general escape hatch (see WorkerConfig.Params). This file is the single
// place that owns the projection between the two: paramsShape is the canonical
// shape both directions drive off, so adding a field means editing one struct,
// not keeping a hand-written mirror in sync.
package project

import (
	"encoding/json"
	"log"
	"sort"

	"github.com/niq-run/niq/core/itfs/worker"
)

// paramsShape is the projection of WorkerConfig's typed fields onto the params
// map. Both workerConfigParams (typed → params) and workerConfigFromParams
// (params → typed) drive off it. The omitempty tags mirror the forward
// direction's "only carry non-empty fields" rule: marshaling a paramsShape
// yields exactly the map workerConfigParams is defined to build. The api_key
// field is deliberately excluded from the reverse path (see
// workerConfigFromParams): secrets never survive an export.
type paramsShape struct {
	Instruction   string             `json:"instruction,omitempty"`
	Provider      string             `json:"provider,omitempty"`
	APIKey        string             `json:"api_key,omitempty"`
	BaseURL       string             `json:"base_url,omitempty"`
	Model         string             `json:"model,omitempty"`
	Subscriptions []SubscriptionSpec `json:"subscriptions,omitempty"`
	Publish       []PublishSpec      `json:"publish,omitempty"`
	Mounts        []string           `json:"mounts,omitempty"`
	Approver      string             `json:"approver,omitempty"`
	Programs      []ProgramSpec      `json:"programs,omitempty"`
}

// exportableParams is what paramsShape can carry; anything else in a worker's
// persisted params (goal/brief/programs/context tuning — spawn-tool extras)
// has no template field and is dropped on export with a log line.
var exportableParams = map[string]bool{
	"instruction": true, "provider": true, "api_key": true, "base_url": true,
	"model": true, "subscriptions": true, "publish": true, "mounts": true,
	"approver": true,
}

// workerConfigParams converts a WorkerConfig into the Params map consumed by
// the builders. It derives the map from paramsShape rather than assembling it
// by hand, so the forward and reverse (export) directions stay in lockstep on
// one struct.
func workerConfigParams(wc WorkerConfig) map[string]any {
	shape := paramsShape{
		Instruction:   wc.Instruction,
		Provider:      wc.Provider,
		APIKey:        wc.APIKey,
		BaseURL:       wc.BaseURL,
		Model:         wc.Model,
		Subscriptions: wc.Subscriptions,
		Publish:       wc.Publish,
		Mounts:        wc.Mounts,
		Approver:      wc.Approver,
		Programs:      wc.Programs,
	}
	raw, err := json.Marshal(shape)
	if err != nil {
		// A struct of primitives cannot fail to marshal; keep the old behavior
		// of returning all-empty on the impossible path.
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// workerParams returns the params map a worker is actually built from: the
// typed fields lowered to params, overlaid with Params. The typed fields are
// the authored view (template, WebUI form); Params is what the builders read
// and the only place arbitrary per-type keys can live — so it wins.
func workerParams(wc WorkerConfig) map[string]any {
	p := workerConfigParams(wc)
	for k, v := range wc.Params {
		p[k] = v
	}
	return p
}

// spawnConfig lowers a declared worker into the workerhost-level config.
func spawnConfig(wc WorkerConfig) worker.WorkerConfig {
	return worker.WorkerConfig{ID: wc.ID, Type: wc.Type, Params: workerParams(wc)}
}

// declFromSpawn turns a runtime-spawned worker's config into a project.json
// declaration. The params map is stored verbatim — it is the only place
// arbitrary per-type keys (spawn-time goal/brief/programs, third-party worker
// types) can live. Display metadata (tags / description) from the spawn request
// is carried on typed fields for the WebUI selector/filter.
func declFromSpawn(cfg worker.WorkerConfig) WorkerConfig {
	return WorkerConfig{Type: cfg.Type, ID: cfg.ID, Params: cfg.Params,
		Tags: cfg.Tags, Description: cfg.Description}
}

// workerConfigFromParams converts a declared worker's effective params back
// into the template WorkerConfig it was seeded from. The params map travels
// through JSON (not hand-parsed) so the subscription/publish entries reuse
// their own unmarshalers. The api_key is deliberately not carried over; with
// withProgram the programs param is lifted into the Programs field.
func workerConfigFromParams(cfg worker.WorkerConfig, withProgram bool) (WorkerConfig, error) {
	raw, err := json.Marshal(cfg.Params)
	if err != nil {
		return WorkerConfig{}, err
	}
	var shape paramsShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		return WorkerConfig{}, err
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
