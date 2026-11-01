// niq — neural interface quantum.
//
// Usage:
//
//	niq                     — start the control plane in the background (default :9527),
//	                          or show its status when it is already running
//	niq control [-f]        — start it in the foreground instead
//	niq status              — report whether it is running and where
//	niq stop [--all]        — stop the background control plane (-all: + projects)
//	niq restart             — stop and start it again
//	niq project             — pick a project, then stop or restart it
//	niq project list        — list projects, their ports and run state
//	niq project create <id> — create a project from a template
//	niq project run <id>    — run a project instance in this process
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/niq-run/niq/app/project"
)

// version is injected at build time via -ldflags:
//
//	-X main.version=v1.2.3
var version = "dev"

func main() {
	// Handle --version / -v / version before anything else.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			fmt.Println("niq", version)
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
	case len(os.Args) > 1 && os.Args[1] == "project":
		err = runProject(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "stop":
		err = runStop(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "restart":
		err = restartControl()
	case len(os.Args) > 1 && os.Args[1] == "status":
		err = statusControl()
	case len(os.Args) > 1:
		err = runControl(os.Args[1:])
	default:
		err = runDefault()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runDefault makes a bare `niq` idempotent: it starts the control plane when
// nothing is running, and reports on it (exactly what `niq status` prints) when
// it is. Repeating the command is therefore how you check on the service, and
// no flags means "the default address", never "a second instance".
func runDefault() error {
	if st, ok := readControlState(); ok && isRunning(st) {
		return statusControl()
	}
	return startBackground(defaultAddr, "")
}

func runControl(args []string) error {
	// Accept the explicit "control" / "start" word, which for historical
	// reasons can precede the flags; flag.Parse would otherwise stop there and
	// silently ignore --addr/--auth.
	if len(args) > 0 && (args[0] == "control" || args[0] == "start") {
		args = args[1:]
	}
	fs := flag.NewFlagSet("niq", flag.ContinueOnError)
	addr := fs.String("addr", defaultAddr, "Control-plane listen address")
	auth := fs.String("auth", "", "Basic auth 'user:pass' for non-localhost access; persists to ~/.niq/common/auth.json (empty = read that file)")
	foreground := fs.Bool("foreground", false, "Run attached to this terminal instead of in the background")
	fs.BoolVar(foreground, "f", false, "Shorthand for --foreground")
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err) // --help already printed the usage
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unknown command %q (try: niq --help)", fs.Arg(0))
	}
	if *foreground {
		return runControlForeground(*addr, *auth)
	}
	return startBackground(*addr, *auth)
}

func runStop(args []string) error {
	fs := flag.NewFlagSet("niq stop", flag.ContinueOnError)
	all := fs.Bool("all", false, "Also stop every running project instance")
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err)
	}
	return stopControl(*all)
}

// helpOrErr keeps --help from being reported as a failure: flag has already
// printed the usage text by the time it returns ErrHelp.
func helpOrErr(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func runProject(args []string) error {
	if len(args) == 0 {
		return runProjectPicker()
	}
	fs := flag.NewFlagSet("niq project", flag.ContinueOnError)
	switch {
	case len(args) >= 1 && args[0] == "list":
		projects, err := project.ListProjects()
		if err != nil {
			return err
		}
		printProjectList(projects)
		return nil

	case len(args) >= 2 && args[0] == "create":
		id := args[1]
		templateName := fs.String("template", "default", "Template to create the project from")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		path := project.ProjectPath(id)
		if _, err := project.LoadProject(id); err == nil {
			return fmt.Errorf("project %q already exists at %s", id, path)
		}
		tmpl, err := project.LoadTemplate(project.TemplatesDir(), *templateName)
		if err != nil {
			return err
		}
		p, err := project.CreateProject(id, *templateName, tmpl)
		if err != nil {
			return err
		}
		fmt.Printf("created project %q -> %s\n", p.ID, path)
		return nil

	case len(args) >= 2 && args[0] == "run":
		id := args[1]
		busAddr := fs.String("bus", "", "httptrans bus listen address (empty reuses the project's persisted port)")
		webUIAddr := fs.String("webui", "", "WebUI listen address (empty reuses the project's persisted port)")
		webUIAuth := fs.String("webui-auth", "", "Basic auth 'user:pass' for non-localhost WebUI access; persists to ~/.niq/common/auth.json (empty = read that file)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		setupProjectLogging(id)
		return project.RunProject(project.ProjectRunOptions{
			ProjectID: id,
			BusAddr:   *busAddr,
			WebUIAddr: *webUIAddr,
			WebUIAuth: *webUIAuth,
		})

	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		fmt.Print(`niq project - create, list and manage project instances

  niq project                      pick a project (↑/↓), then stop or restart it
  niq project list                 list projects, their ports and run state
  niq project create <id> [--template <name>]
                                   create a project from a template
  niq project run <id> [--bus :0] [--webui :0] [--webui-auth user:pass]
                                   run an instance in this terminal
`)
		return nil

	default:
		return fmt.Errorf("usage: niq project [list | create <id> | run <id>] (see: niq project --help)")
	}
}

// printProjectList is the plain listing: `niq project list`, and what a bare
// `niq project` degrades to when stdin is not a terminal.
func printProjectList(projects []project.Project) {
	if len(projects) == 0 {
		fmt.Println("(no projects)")
		return
	}
	for _, p := range projects {
		fmt.Printf("%-24s webui=%-6d bus=%-6d %s\n", p.ID, p.Ports.WebUI, p.Ports.Bus, projectState(p))
	}
}

// setupLogging sends control-plane logs to <niq root>/niq.log.
func setupLogging() {
	path := controlLogPath()
	os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		log.SetOutput(f)
	} else {
		log.SetOutput(io.Discard)
	}
	log.SetPrefix("[niq] ")
}

// setupProjectLogging sends this project process's logs to
// ~/.niq/projects/<id>/logs/niq-YYYY-MM-DD.log. The date in the filename gives
// free daily rotation: a new day starts a new file, same-day restarts append.
func setupProjectLogging(id string) {
	path := project.LogPath(id)
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.SetOutput(io.Discard)
		return
	}
	log.SetOutput(f)
	log.SetPrefix("[niq] ")
}

func printUsage() {
	fmt.Print(`niq - neural interface quantum

Usage:
  niq                       start the control plane (default :9527) in the
                            background, print its WebUI address, and return to
                            the prompt; if it is already running, show the same
                            report as 'niq status'. Its WebUI also serves every
                            project at /p/<id>/. Closing the terminal does not
                            stop it.
  niq control --foreground  (or -f) run it attached to this terminal instead
  niq status                show the control plane and each project's run state
                            (bare 'niq' does this once the plane is up)
  niq stop                  stop the background control plane; projects it
                            serves are separate processes and keep running
  niq stop --all            also stop every running project instance
  niq restart               restart it on the address it was started with
  niq --addr :9527 --auth user:pass
                            start on another address, locking non-localhost
                            access behind basic auth (saved to
                            ~/.niq/common/auth.json, reused on later runs)
  niq project               pick a project (↑/↓), then stop or restart it
  niq project list          list projects, their ports and run state
  niq project create <id> [--template <name>]
                            create a project from a template
  niq project run <id> [--bus :0] [--webui :0] [--webui-auth user:pass]
                            run a project instance in this process (webui-auth
                            locks non-localhost WebUI access behind basic auth)
  niq --version             print version
`)
}
