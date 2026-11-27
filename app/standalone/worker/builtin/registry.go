// Package builtin is the framework of the standalone form's type table: the
// registry a type registers into, the assembly context a builder reads, the
// spawn-parameter contract machinery, and the bus identity a builder ensures.
//
// The types themselves are in app/standalone/worker/builtin/workers, one file
// each. The split is the point: a table is "a registry plus entries", and a
// different deployment form can keep this framework and ship a different set of
// entries without touching it.
//
// Above this package sits `app/standalone/worker` (the control plane: manifest
// model, placement routing, lifecycle), which knows a type only as a label it
// passes down. Below it sit the worker implementations themselves
// (`core/impl/workers/*`) and `worker/process` for the types that need an OS
// child.
package builtin

import (
	"fmt"
	"sort"
	"sync"

	"github.com/niq-run/niq/core/impl/workerctl"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// SpecFunc builds one worker type. The assembly's context is the first
// argument so a type's file can register itself without closing over it.
type SpecFunc func(BuildContext, coreworker.WorkerConfig) (coreworker.SpawnSpec, error)

// Registry is the type table: which types exist, how each is built, and which
// spawn parameters each accepts.
//
// workerctl.Manager receives exactly one Build function and never learns that
// types exist; this table is the layer that decides what a project may spawn.
type Registry struct {
	mu       sync.RWMutex
	builders map[string]workerctl.Builder
	checks   map[string]func(params map[string]any) error
	accepted map[string][]string
	// specs is the full contract a type registered, carrying each param's
	// secrecy. accepted/secret are derived views of it, kept so the control
	// plane and tests can read the same registration.
	specs map[string][]ParamSpec
	// secret names the params that carry a credential. Declaring it is how the
	// write path knows to seal a literal into the vault instead of writing it to
	// workers.json — a fact about the type, not a guess about a param's name.
	secret map[string][]string
}

// NewRegistry returns an empty table.
func NewRegistry() *Registry {
	return &Registry{
		builders: map[string]workerctl.Builder{},
		checks:   map[string]func(map[string]any) error{},
		accepted: map[string][]string{},
		specs:    map[string][]ParamSpec{},
		secret:   map[string][]string{},
	}
}

// RegisterType records one built-in type: its builder, the parameters it
// accepts, and the check derived from that list.
//
// The contract is registered with the builder, in one call, because the valid
// parameter set is the type's own knowledge: drift between "what the builder
// reads" and "what the check allows" is then a visible edit rather than a silent
// gap. The same contract carries each param's secrecy, so a credential param is
// declared in one place rather than a second, forgettable MarkSecret call.
func (r *Registry) RegisterType(typ string, specs []ParamSpec, ctx BuildContext, build SpecFunc) {
	keys := Names(specs)
	r.RegisterBuilder(typ, func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return build(ctx, cfg)
	}, CheckSpawnParams(typ, specs))

	specsCopy := append([]ParamSpec(nil), specs...)
	r.mu.Lock()
	r.accepted[typ] = keys
	r.specs[typ] = specsCopy
	r.mu.Unlock()
	if secrets := SecretNames(specs); len(secrets) > 0 {
		r.MarkSecret(typ, secrets...)
	}
}

// MarkSecret declares which of a type's params carry a credential. The control
// plane seals a literal value for these keys into the vault and stores only a
// `vault:` reference, so a token never lands in workers.json.
//
// RegisterType derives this from the type's ParamSpecs; the call remains public
// as the seam for RegisterBuilder types (out-of-tree, tests) that have no spec
// list to carry secrecy.
func (r *Registry) MarkSecret(typ string, names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secret[typ] = append([]string(nil), names...)
}

// SecretParams returns the credential-bearing params of a type (empty when none).
func (r *Registry) SecretParams(typ string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.secret[typ]...)
}

// Specs returns the contract a type registered with, sorted by name. A type
// registered through RegisterBuilder (out-of-tree, tests) reports nil, false:
// it has no spec list, only whatever check it supplied.
func (r *Registry) Specs(typ string) ([]ParamSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	specs, ok := r.specs[typ]
	if !ok {
		return nil, false
	}
	out := append([]ParamSpec(nil), specs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, true
}

// RegisterBuilder is the seam for types this package does not ship: tests and
// out-of-tree worker types. A nil check accepts any parameters.
func (r *Registry) RegisterBuilder(typ string, b workerctl.Builder, check func(map[string]any) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builders[typ] = b
	if check != nil {
		r.checks[typ] = check
	}
}

// Build implements workerctl.Builder.
func (r *Registry) Build(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	r.mu.RLock()
	b, ok := r.builders[cfg.Type]
	r.mu.RUnlock()
	if !ok {
		return coreworker.SpawnSpec{}, fmt.Errorf("no builder for type %q", cfg.Type)
	}
	return b(cfg)
}

// Validate reports whether a config could be built: a builder exists for its
// type and that type's spawn-parameter check passes. Callers run it before
// persisting a manifest, so a rejected spawn leaves nothing behind. Recovery
// deliberately skips it: a manifest already accepted must not be stranded by a
// stricter check added later.
func (r *Registry) Validate(cfg coreworker.WorkerConfig) error {
	r.mu.RLock()
	_, ok := r.builders[cfg.Type]
	check := r.checks[cfg.Type]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no builder for type %q", cfg.Type)
	}
	if check != nil {
		if err := check(cfg.Params); err != nil {
			return err
		}
	}
	return nil
}

// Types returns the registered type labels, sorted.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.builders))
	for typ := range r.builders {
		types = append(types, typ)
	}
	sort.Strings(types)
	return types
}

// Accepted returns the spawn parameters recorded for a type, sorted. A type
// registered without a contract (the out-of-tree seam) reports nil.
func (r *Registry) Accepted(typ string) ([]string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys, ok := r.accepted[typ]
	if !ok {
		return nil, false
	}
	return append([]string(nil), keys...), true
}
