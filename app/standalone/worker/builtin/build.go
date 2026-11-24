package builtin

import (
	providerpkg "github.com/niq-run/niq/app/standalone/provider"
	"github.com/niq-run/niq/core/impl/eventbus"
	eventbusapi "github.com/niq-run/niq/core/impl/eventbus/api"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/workerctl"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

type BuildContext struct {
	Registry   corebus.IdentityRegistry
	Bus        corebus.Control
	Listener   *inprocess.InProcListener
	Engine     *eventbus.Engine
	Manager    *workerctl.Manager
	EventLog   *eventbusapi.EventLog
	EventStore event.EventStore
	// ProviderLoad reads provider.json. It is injected (store.LoadProvider in
	// production) so builders never resolve the config path or read the file.
	ProviderLoad providerpkg.Loader
	ProgramsRoot string
	// StateDir is the per-project workers state directory (`<proj>/workers`);
	// built-in workers persist their own state here (one dir per id).
	StateDir string
	// WorkerBackend is the control plane the host worker drives: the same
	// instance the assembly uses for recovery and shutdown.
	WorkerBackend coreworker.Backend
}
