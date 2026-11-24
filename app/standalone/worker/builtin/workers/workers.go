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
