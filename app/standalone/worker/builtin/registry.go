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
}

// NewRegistry returns an empty table.
func NewRegistry() *Registry {
	return &Registry{
		builders: map[string]workerctl.Builder{},
		checks:   map[string]func(map[string]any) error{},
		accepted: map[string][]string{},
	}
}

// RegisterType records one built-in type: its builder, the parameters it
// accepts, and the check derived from that list.
//
// The contract is registered with the builder, in one call, because the valid
// parameter set is the type's own knowledge: drift between "what the builder
// reads" and "what the check allows" is then a visible edit rather than a silent
// gap.
func (r *Registry) RegisterType(typ string, accepted []string, ctx BuildContext, build SpecFunc) {
	keys := append([]string(nil), accepted...)
	r.RegisterBuilder(typ, func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return build(ctx, cfg)
	}, CheckSpawnParams(typ, keys))

	r.mu.Lock()
	r.accepted[typ] = keys
	r.mu.Unlock()
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
