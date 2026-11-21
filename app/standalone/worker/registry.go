// The worker-type registry: which types exist, how each is constructed, and
// which spawn parameters each reads.
//
// This is assembly knowledge, not mechanism. workerctl.Manager receives one
// Build function and never learns that types exist; the control plane owns the
// table because it is the layer that decides what a project may spawn.
package worker

import (
	"fmt"
	"sort"
	"sync"

	"github.com/niq-run/niq/core/impl/workerctl"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// typeRegistry maps a type label to its builder and spawn-parameter check.
type typeRegistry struct {
	mu          sync.RWMutex
	builders    map[string]workerctl.Builder
	paramChecks map[string]func(params map[string]any) error
}

func newTypeRegistry() *typeRegistry {
	return &typeRegistry{
		builders:    map[string]workerctl.Builder{},
		paramChecks: map[string]func(map[string]any) error{},
	}
}

// register adds a type's builder and, optionally, its spawn-parameter check.
// The check is registered next to the builder because the valid parameter set
// is that type's own knowledge.
func (r *typeRegistry) register(typ string, b workerctl.Builder, check func(map[string]any) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builders[typ] = b
	if check != nil {
		r.paramChecks[typ] = check
	}
}

// Build implements workerctl.Builder.
func (r *typeRegistry) Build(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
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
func (r *typeRegistry) Validate(cfg coreworker.WorkerConfig) error {
	r.mu.RLock()
	_, ok := r.builders[cfg.Type]
	check := r.paramChecks[cfg.Type]
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
func (r *typeRegistry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.builders))
	for typ := range r.builders {
		types = append(types, typ)
	}
	sort.Strings(types)
	return types
}
