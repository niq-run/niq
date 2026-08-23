// niq — neural interface quantum.
//
// Usage:
//
//	niq                       — start with the default "dev" preset
//	niq swarm --config <file> — start from a YAML config file
//	niq swarm --preset <name> — start from a built-in preset
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/54c1/niq/internal/swarm"
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

	// Detect subcommand: "niq control ..." (control-plane, default port 9527)
	if len(os.Args) > 1 && os.Args[1] == "control" {
		if err := runControl(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Detect subcommand: "niq project ..."
	if len(os.Args) > 1 && os.Args[1] == "project" {
		if err := runProject(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Detect subcommand: "niq swarm ..."
	if len(os.Args) > 1 && os.Args[1] == "swarm" {
		if err := runSwarm(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Default: no args, start with the dev preset.
	// Also support -config / -preset as top-level flags for convenience.
	if err := runSwarm(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runControl(args []string) error {
	fs := flag.NewFlagSet("niq control", flag.ContinueOnError)
	addr := fs.String("addr", ":9527", "Control-plane listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return swarm.RunControl(swarm.ControlOptions{Addr: *addr})
}

func runProject(args []string) error {
	fs := flag.NewFlagSet("niq project", flag.ContinueOnError)
	switch {
	case len(args) >= 1 && args[0] == "list":
		projects, err := swarm.ListProjects()
		if err != nil {
			return err
		}
		if len(projects) == 0 {
			fmt.Println("(no projects)")
			return nil
		}
		for _, p := range projects {
			fmt.Printf("%-24s webui=%d bus=%d workers=%d\n", p.ID, p.Ports.WebUI, p.Ports.Bus, len(p.Workers))
		}
		return nil

	case len(args) >= 2 && args[0] == "create":
		id := args[1]
		templateName := fs.String("template", "dev", "Template to create the project from")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		path := swarm.ProjectPath(id)
		if _, err := swarm.LoadProject(id); err == nil {
			return fmt.Errorf("project %q already exists at %s", id, path)
		}
		tmpl, err := swarm.LoadTemplate(swarm.TemplatesDir(), *templateName)
		if err != nil {
			return err
		}
		p, err := swarm.CreateProject(id, tmpl)
		if err != nil {
			return err
		}
		fmt.Printf("created project %q (%d workers) -> %s\n", p.ID, len(p.Workers), path)
		return nil

	case len(args) >= 1 && args[0] == "migrate":
		id := "default"
		if len(args) >= 2 {
			id = args[1]
		}
		home, _ := os.UserHomeDir()
		projDir, err := swarm.MigrateLegacyProject(id, filepath.Join(home, ".niq"))
		if err != nil {
			return err
		}
		fmt.Printf("migrated legacy swarm into project %q -> %s\n", id, projDir)
		return nil

	case len(args) >= 2 && args[0] == "run":
		id := args[1]
		busAddr := fs.String("bus", ":0", "httptrans bus listen address")
		webUIAddr := fs.String("webui", ":0", "WebUI listen address")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		return swarm.RunProject(swarm.ProjectRunOptions{
			ProjectID: id,
			BusAddr:   *busAddr,
			WebUIAddr: *webUIAddr,
		})

	default:
		return fmt.Errorf("usage: niq project list | niq project create <id> [--template <name>] | niq project run <id> [--bus :0] [--webui :0]")
	}
}

func printUsage() {
	fmt.Print(`niq - neural interface quantum

Usage:
  niq                       start with the default "dev" preset
  niq swarm --config <file> start from a YAML config file
  niq swarm --preset <name> start from a built-in preset
  niq --version             print version
`)
}

func runSwarm(args []string) error {
	// Set up logging to ~/.niq/niq.log.
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".niq")
	os.MkdirAll(logDir, 0755)
	f, err := os.OpenFile(filepath.Join(logDir, "niq.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		log.SetOutput(f)
		defer f.Close()
	} else {
		log.SetOutput(io.Discard)
	}
	log.SetPrefix("[niq] ")

	fs := flag.NewFlagSet("niq", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to swarm YAML config")
	preset := fs.String("preset", "", "Built-in preset name (dev, test-headless, etc.)")
	webUIAddr := fs.String("webui", ":19763", "WebUI listen address (e.g. :19763)")
	programsRoot := fs.String("programs-root", "", "Program storage root directory (default: ~/.niq/programs)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return swarm.RunSwarm(swarm.RunOptions{
		ConfigPath:   *configPath,
		Preset:       *preset,
		WebUIAddr:    *webUIAddr,
		ProgramsRoot: *programsRoot,
	})
}
