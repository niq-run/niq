// niq — neural interface quantum.
//
// Usage:
//
//	niq                     — start the control plane (default :9527)
//	niq project list        — list projects and their ports
//	niq project create <id> — create a project from a template
//	niq project run <id>    — run a project instance in this process
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/niq-run/niq/internal/control"
	"github.com/niq-run/niq/internal/project"
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

	// Detect subcommand: "niq project ..."
	if len(os.Args) > 1 && os.Args[1] == "project" {
		if err := runProject(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// "niq control ..." — explicit control-plane start; bare "niq" is the
	// shorthand for the same thing.
	if err := runControl(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runControl(args []string) error {
	fs := flag.NewFlagSet("niq", flag.ContinueOnError)
	addr := fs.String("addr", ":9527", "Control-plane listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	setupLogging()
	return control.RunControl(control.ControlOptions{Addr: *addr})
}

func runProject(args []string) error {
	fs := flag.NewFlagSet("niq project", flag.ContinueOnError)
	switch {
	case len(args) >= 1 && args[0] == "list":
		projects, err := project.ListProjects()
		if err != nil {
			return err
		}
		if len(projects) == 0 {
			fmt.Println("(no projects)")
			return nil
		}
		for _, p := range projects {
			fmt.Printf("%-24s webui=%d bus=%d\n", p.ID, p.Ports.WebUI, p.Ports.Bus)
		}
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
		p, err := project.CreateProject(id, tmpl)
		if err != nil {
			return err
		}
		fmt.Printf("created project %q -> %s\n", p.ID, path)
		return nil

	case len(args) >= 2 && args[0] == "run":
		id := args[1]
		busAddr := fs.String("bus", "", "httptrans bus listen address (empty reuses the project's persisted port)")
		webUIAddr := fs.String("webui", "", "WebUI listen address (empty reuses the project's persisted port)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		setupProjectLogging(id)
		return project.RunProject(project.ProjectRunOptions{
			ProjectID: id,
			BusAddr:   *busAddr,
			WebUIAddr: *webUIAddr,
		})

	default:
		return fmt.Errorf("usage: niq project list | niq project create <id> [--template <name>] | niq project run <id> [--bus :0] [--webui :0]")
	}
}

// setupLogging sends control-plane logs to ~/.niq/niq.log.
func setupLogging() {
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".niq")
	os.MkdirAll(logDir, 0755)
	f, err := os.OpenFile(filepath.Join(logDir, "niq.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
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
	logDir := filepath.Join(project.ProjectDir(id), "logs")
	_ = os.MkdirAll(logDir, 0755)
	daily := "niq-" + time.Now().Format("2006-01-02") + ".log"
	f, err := os.OpenFile(filepath.Join(logDir, daily), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
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
  niq                       start the control plane (default :9527)
  niq control --addr :9527  start the control plane explicitly
  niq project list          list projects and their ports
  niq project create <id> [--template <name>]
                            create a project from a template
  niq project run <id> [--bus :0] [--webui :0]
                            run a project instance in this process
  niq --version             print version
`)
}
