// niq — neural interface quantum.
//
// Usage:
//
//	niq                     — start the control plane in the background (loopback by
//	                          default, or the remembered address), or show its status
//	                          when it is already running
//	niq w [-f]        — start it in the foreground instead
//	niq status              — report whether it is running and where
//	niq stop [-p]           — stop the background control plane (-p: the projects too)
//	niq restart [-p]        — stop and start it again (-p: the projects too)
//	niq update              — install the latest release with npm
//	niq i                   — open the interactive terminal shell
//	niq w                   — start the browser shell (same as bare niq)
package cli

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/niq-run/niq/app/client/tui"
	"github.com/niq-run/niq/app/standalone/gateway"
	"github.com/niq-run/niq/app/standalone/launcher"
	"github.com/niq-run/niq/app/standalone/store"
)

// Spec is what a distribution decides: its build identity and the static
// interfaces the gateway should serve. publish/cmd fills it; nothing else does.
type Spec struct {
	Version   string
	BuildTime string
	UI        []gateway.UIMount
}

// version and buildTime are injected through Spec by publish/cmd. The ldflags
// still target main.version / main.buildTime, and publish/cmd forwards them.
var (
	version   = "dev"
	buildTime = ""
	ui        []gateway.UIMount
)

// Main runs the CLI with the given spec.
func Main(spec Spec) {
	version, buildTime, ui = spec.Version, spec.BuildTime, spec.UI
	tui.Version = version
	// The launcher's private child entry: this process is a project runtime,
	// not the CLI. Dispatch before anything else so the CLI never sees it.
	if launcher.IsChild(os.Args) {
		if err := launcher.ChildMain(os.Args[2:]); err != nil {
			log.Printf("[niq] %v", err)
			os.Exit(1)
		}
		return
	}

	// Handle --version / -v / version before anything else.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			fmt.Println(versionTag())
			return
		case "--help", "-h", "help":
			printUsage()
			return
		}
	}

	// Dispatch subcommands. Everything else (bare "niq", flags, the explicit
	// "control" word) is a control-plane start.
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "i":
		err = runInteractive()
	case len(os.Args) > 1 && os.Args[1] == "w":
		err = runShell(os.Args[2:], true)
	case len(os.Args) > 1 && os.Args[1] == "stop":
		err = runStop(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "restart":
		err = runRestart(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "status":
		err = runStatus(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "update":
		err = runUpdate(os.Args[2:])
	case len(os.Args) > 1 && (os.Args[1] == "completion" || os.Args[1] == "complete"):
		err = runCompletion(os.Args[2:])
	case len(os.Args) > 1:
		err = runShell(os.Args[1:], false)
	default:
		err = runDefault()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runInteractive opens the terminal shell. Like every other face it needs the
// gateway: the picker is a client, not a second control path.
func runInteractive() error {
	st, ok := readShellState()
	if !ok || !isRunning(st) {
		var err error
		// Honor a remembered bind, but do not prompt: `niq i` is a quick picker,
		// not the place to decide how the plane is exposed.
		addr, err := resolveStartAddr(startRequest{}, nil, authConfigured())
		if err != nil {
			return err
		}
		st, err = ensureGateway(addr, "", askOnTerminal())
		if err != nil {
			return err
		}
	}
	if st == nil {
		if again, ok := readShellState(); ok {
			st = again
		}
	}
	if st == nil {
		return fmt.Errorf("the gateway did not report an address")
	}
	return tui.RunPicker(store.LocalhostURL(st.Bound))
}

// runDefault makes a bare `niq` idempotent: it starts the control plane when
// nothing is running, and reports on it (exactly what `niq status` prints) when
// it is. Repeating the command is therefore how you check on the service, and
// no flags means "the remembered or default address", never "a second
// instance".
func runDefault() error {
	if st, ok := readShellState(); ok && isRunning(st) {
		err := printStatus()
		checkForUpdate()
		return err
	}
	addr, err := resolveStartAddr(startRequest{}, askOnTerminalBind(), authConfigured())
	if err != nil {
		return err
	}
	st, err := ensureGateway(addr, "", askOnTerminal())
	if err != nil {
		return err
	}
	if st != nil {
		printShellStarted(st)
	}
	checkForUpdate()
	return nil
}

func runShell(args []string, openBrowser bool) error {
	// Accept the explicit "control" / "start" word, which for historical
	// reasons can precede the flags; flag.Parse would otherwise stop there and
	// silently ignore --addr/--auth.
	if len(args) > 0 && (args[0] == "w" || args[0] == "start") {
		args = args[1:]
	}
	fs := flag.NewFlagSet("niq", flag.ContinueOnError)
	addr := fs.String("addr", defaultAddr, "Control-plane listen address (a non-loopback bind is unauthenticated unless --auth is set)")
	auth := fs.String("auth", "", "Basic auth 'user:pass' for non-localhost access; persists to ~/.niq/common/auth.json (empty = read that file)")
	remote := fs.Bool("remote", false, "Listen on all interfaces for this run (requires --auth or saved credentials)")
	local := fs.Bool("local", false, "Listen on loopback only for this run, ignoring a remembered remote address")
	foreground := fs.Bool("foreground", false, "Run attached to this terminal instead of in the background")
	fs.BoolVar(foreground, "f", false, "Shorthand for --foreground")
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err) // --help already printed the usage
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unknown command %q (try: niq --help)", fs.Arg(0))
	}

	// Was --addr actually passed? "Not set" and "set to the default" must stay
	// distinguishable, otherwise a remembered preference could never apply.
	addrSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			addrSet = true
		}
	})
	req := startRequest{Remote: *remote, Local: *local}
	if addrSet {
		req.Addr = *addr
	}
	if err := req.validate(); err != nil {
		return err
	}
	// --auth on this invocation counts as configured even before the daemon
	// writes it to common/auth.json (StartupAuth does that on startup).
	authReady := *auth != "" || authConfigured()
	running := gatewayRunning()

	if *foreground {
		// A gateway of ours is not a port conflict: keep the existing
		// "already running" answer instead of offering to stop the recorded
		// pid. runShellForeground owns that check and its message, so the bind
		// is resolved (and possibly asked about) only when it will be bound.
		if running {
			// Resolved without asking: it only matters if the plane stopped in
			// the meantime, in which case binding the remembered address is right.
			return runShellForeground(requestedAddr(req), *auth)
		}
		resolved, err := resolveStartAddr(req, askOnTerminalBind(), authReady)
		if err != nil {
			return err
		}
		newAddr, cancelled, err := resolvePortConflict(resolved, askOnTerminal(), defaultPortOps)
		if err != nil || cancelled {
			return err
		}
		return runShellForeground(newAddr, *auth)
	}

	// Detached start. When our own plane is already serving, ensureGateway
	// reports it and the bind choice cannot take effect, so nothing is asked —
	// but the address is still projected so the report compares against the
	// address this start wanted, not against the default.
	resolved := requestedAddr(req)
	if !running {
		var err error
		resolved, err = resolveStartAddr(req, askOnTerminalBind(), authReady)
		if err != nil {
			return err
		}
	}
	st, err := ensureGateway(resolved, *auth, askOnTerminal())
	if err != nil {
		return err
	}
	if st != nil {
		printShellStarted(st)
		if openBrowser {
			openInBrowser(store.LocalhostURL(st.Bound))
		}
	}
	return nil
}

// runRestart restarts the control plane. -p / --projects also restarts the
// instances that are running — after a binary upgrade each of them still serves
// the page it was built with.
func runRestart(args []string) error {
	fs := flag.NewFlagSet("niq restart", flag.ContinueOnError)
	var projects bool
	fs.BoolVar(&projects, "projects", false, "Also restart every running project instance")
	fs.BoolVar(&projects, "p", false, "Shorthand for --projects")
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err)
	}
	return restartControl(projects)
}

func runStop(args []string) error {
	fs := flag.NewFlagSet("niq stop", flag.ContinueOnError)
	var projects bool
	fs.BoolVar(&projects, "projects", false, "Also stop every running project instance")
	fs.BoolVar(&projects, "p", false, "Shorthand for --projects")
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err)
	}
	return stopShell(projects)
}

// helpOrErr keeps --help from being reported as a failure: flag has already
// printed the usage text by the time it returns ErrHelp.
func helpOrErr(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func printUsage() {
	printBanner(banner{tagline: dim("neural interface quantum")})
	fmt.Println()
	fmt.Printf("%s\n", bracketed("usage"))
	printUsageRows([][2]string{
		{"niq", "start the control plane in the background, or report on it when it is already running"},
		{"niq w [-f]", "start the browser shell (foreground with -f)"},
		{"niq status", "the control plane and every project instance"},
		{"niq stop [-p]", "stop the control plane; -p also stops the running projects"},
		{"niq restart [-p]", "restart the control plane; -p also restarts the running projects"},
		{"niq update", "install the latest release with npm"},
		{"niq i", "open the interactive terminal shell"},
		{"niq completion <shell>", "print a completion script: bash, zsh or fish"},
		{"niq --version", "print the version"},
	})

	fmt.Printf("\n%s\n", bracketed("start flags"))
	printUsageRows([][2]string{
		{"--remote", "listen on all interfaces for this run; requires --auth (or saved credentials)"},
		{"--local", "listen on loopback only for this run, ignoring a remembered remote address"},
		{"--addr <host:port>", "explicit listen address; overrides the remembered default (loopback " + defaultAddr + " when unset)"},
		{"--auth <user:pass>", "basic auth for non-localhost access; saved to " + shortPath(store.AuthConfigPath())},
	})

	// Files, not just flags: a bare `niq` asks once and remembers the answer,
	// so the place it lives has to be discoverable from --help alone.
	fmt.Printf("\n%s\n", bracketed("config files"))
	printUsageRows([][2]string{
		{shortPath(store.ConfigPath()), `{"default_addr": ":9527"} — remembered listen address; delete the file to be asked again`},
		{shortPath(store.AuthConfigPath()), `{"user": "alice", "pass": "s3cret"} — basic auth for non-localhost access`},
	})

	fmt.Printf("\n%s\n", bracketed("shell completion"))
	printUsageRows([][2]string{
		{"niq completion install", "write it into your shell's own directory — nothing to add to a dotfile"},
		{"bash", "source <(niq completion bash)"},
		{"zsh", "niq completion zsh > \"${fpath[1]}/_niq\""},
		{"fish", "niq completion fish | source"},
	})
}

// printUsageRows lays out command/description pairs: a fixed column keeps the
// table aligned, and a command too long for it gets its description on the next
// line instead of pushing the whole table right.
func printUsageRows(rows [][2]string) {
	const col = 30
	for _, r := range rows {
		if len(r[0]) <= col {
			fmt.Printf("%s  %s\n", bold(fmt.Sprintf("%-*s", col, r[0])), dim(r[1]))
			continue
		}
		fmt.Printf("%s\n", bold(r[0]))
		fmt.Printf("%s  %s\n", strings.Repeat(" ", col), dim(r[1]))
	}
}
