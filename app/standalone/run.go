// Package standalone is the single-machine form of niq: the composition root
// and the entry that assembles it.
//
// A distribution declares what it ships — build identity and the UIs the
// gateway should mount — and this package hands that to the CLI. Assembly
// itself stays with the process that owns it: the gateway process builds the
// store, launcher and HTTP surface; a project process builds its bus and
// workers.
package standalone

import (
	"github.com/niq-run/niq/app/standalone/cli"
	"github.com/niq-run/niq/app/standalone/gateway"
)

// Spec is what a distribution decides: which build this is, and which static
// UIs the gateway serves. Nothing here reaches into the domains.
type Spec struct {
	Version   string
	BuildTime string
	UI        []gateway.UIMount
}

// Main runs the CLI with the given spec. publish/cmd is the only caller.
func Main(spec Spec) {
	cli.Version = spec.Version
	cli.BuildTime = spec.BuildTime
	cli.UI = spec.UI
	cli.Main()
}
