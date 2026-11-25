// Package workers holds one file per built-in worker type: the spawn
// parameters that type accepts, the builder that turns them into a
// SpawnSpec, and the registration that pairs the two.
//
// It is the "entries" half of the type table; the framework it registers into
// — Registry, BuildContext, the contract machinery, the identity a builder
// ensures — is `app/standalone/worker/builtin` above it. Splitting them is what
// lets the table be swapped: a different deployment form can keep the
// framework and ship a different set of types.
//
// Register is the explicit list of what this form ships. Adding a type is
// adding a file here and a line in that list.
package workers

import (
	"github.com/niq-run/niq/app/standalone/worker/builtin"
)

// Register adds one builder per known worker type, each with its spawn-
// parameter contract. The list is explicit rather than discovered, so what a
// project can spawn is one edit to read.
func Register(ctx builtin.BuildContext, reg *builtin.Registry) {
	registerReason(ctx, reg)
	registerCoding(ctx, reg)
	registerWorkspace(ctx, reg)
	registerHost(ctx, reg)
	registerTimer(ctx, reg)
	registerNiq(ctx, reg)
	registerProgram(ctx, reg)
	registerHistory(ctx, reg)
	registerDirectory(ctx, reg)
	registerMCP(ctx, reg)
}
