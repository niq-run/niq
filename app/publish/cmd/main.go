// Command niq is the product entry: a thin main that injects the build
// identity, declares the UIs this distribution ships, and hands both to the
// standalone form.
package main

import (
	"github.com/niq-run/niq/app/standalone"
	"github.com/niq-run/niq/app/standalone/gateway"
	"github.com/niq-run/niq/app/ui/webui"
)

// Injected at build time via -ldflags -X main.version / -X main.buildTime.
var (
	version   = "dev"
	buildTime = ""
)

func main() {
	standalone.Main(standalone.Spec{
		Version:   version,
		BuildTime: buildTime,
		UI:        []gateway.UIMount{{Prefix: "/", FS: webui.Assets()}},
	})
}
