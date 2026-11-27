package builtin

import (
	providerpkg "github.com/niq-run/niq/app/standalone/provider"
	"github.com/niq-run/niq/app/standalone/vault"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/workerctl"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// BuildContext is everything a builder needs from the assembly: the world it
// wires a worker into. It is filled once, by the project assembly, and bound
// into each type's builder at registration — which is why a builder takes it as
// an argument instead of reaching for a global, a config path or a listener.
//
// A field belongs here only if some builder reads it. The bus and the listener
// connect the worker; the manager lets a worker whose durable state changed ask
// for a checkpoint; the rest are the inputs a particular type builds against
// (provider config, the programs root, the event store) and the lifecycle
// backend the host worker drives.
type BuildContext struct {
	// Bus is the bus identity control plane: a worker's connect path registers
	// its identity through it.
	Bus corebus.Control
	// Listener is the in-process transport the worker's channel attaches to.
	Listener *inprocess.InProcListener
	// Manager is the instance manager, for a builder that persists state at
	// runtime (`Checkpoint`).
	Manager *workerctl.Manager
	// ProviderLoad reads provider.json. It is injected (store.LoadProvider in
	// production) so builders never resolve the config path or read the file.
	ProviderLoad providerpkg.Loader
	// ProgramsRoot is the default mount for a project's programs.
	ProgramsRoot string
	// EventStore is the history a worker reads back (`history`).
	EventStore event.EventStore
	// WorkerBackend is the control plane the host worker drives: the same
	// instance the assembly uses for recovery and shutdown.
	WorkerBackend coreworker.Backend
	// Secrets resolves `env:` / `file:` (and, later, `vault:`) credential
	// references for a builder. Injected so a dedicated provider can replace the
	// default without changing worker code; nil means the env/file default.
	Secrets vault.Provider
}
