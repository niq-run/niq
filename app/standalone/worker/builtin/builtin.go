package builtin

import ()

import ()

// The control-plane workers every project ensures. They are named here because
// the builders default to them, and the assembly references them by these
// constants instead of repeating literals.
const (
	// DefaultWebUIID is the app-hosted interface worker the WebUI drives. It has
	// no declaration and no builder: the assembly joins it directly.
	DefaultWebUIID = "webui"
	// TypeInterface is the type label of an interface worker: a worker whose
	// backend is an external interface, injected by the assembly. It is not
	// spawnable — no backend means nothing to bridge to — so it has no builder.
	TypeInterface = "interface"
	// DefaultHostWorkerID is the lifecycle control plane.
	DefaultHostWorkerID = "host"
)

// Register adds one builder per known worker type, each with its spawn-
// parameter contract. The list is explicit rather than discovered, so what a
// project can spawn is one edit to read.
func Register(ctx BuildContext, reg *Registry) {
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
