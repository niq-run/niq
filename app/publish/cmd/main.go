// Command niq is the product entry: a thin main that injects the build
// identity and hands the argv to the CLI package.
package main

import "github.com/niq-run/niq/app/standalone/cli"

// Injected at build time via -ldflags -X main.version / -X main.buildTime.
var (
	version   = "dev"
	buildTime = ""
)

func main() {
	cli.Version = version
	cli.BuildTime = buildTime
	cli.Main()
}
