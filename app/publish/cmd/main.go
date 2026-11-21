// Command niq is the product entry: a thin main that injects the build
// identity, declares the UIs this distribution ships, and hands both to the
// CLI.
package main

import (
	"github.com/niq-run/niq/app/client/webui"
	"github.com/niq-run/niq/app/standalone/cli"
	"github.com/niq-run/niq/app/standalone/gateway"
)

// Injected at build time via -ldflags -X main.version / -X main.buildTime.
var (
	version   = "dev"
	buildTime = ""
)

func main() {
	cli.Main(cli.Spec{
		Version:   version,
		BuildTime: buildTime,
		UI:        []gateway.UIMount{{Prefix: "/", FS: webui.Assets()}},
	})
}
