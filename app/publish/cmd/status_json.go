// `niq status --json` is the same snapshot the report draws, for callers that
// render their own UI — the macOS menu bar app needs the control plane's state,
// the address that actually reaches it, and one entry per project with the
// things that can be done to it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/niq-run/niq/app/project"
)

type statusSnapshot struct {
	Version  string            `json:"version"`
	Control  controlSnapshot   `json:"control"`
	Projects []projectSnapshot `json:"projects"`
}

type controlSnapshot struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid,omitempty"`
	Addr    string `json:"addr,omitempty"` // as configured, e.g. ":9527"
	URL     string `json:"url,omitempty"`  // as reached, e.g. http://localhost:9527
	Log     string `json:"log"`
	Started string `json:"started,omitempty"`
}

type projectSnapshot struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
	WebUI   int    `json:"webui"`
	Bus     int    `json:"bus"`
	URL     string `json:"url,omitempty"` // proxy when the plane is up, loopback otherwise
	Log     string `json:"log"`
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("niq status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "Print the status as JSON")
	fs.Usage = func() {
		fmt.Println(bold("niq status") + dim(" — the control plane and every project instance"))
		fmt.Println()
		fmt.Println(bold("  --json") + dim("  the same facts, machine-readable"))
	}
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err)
	}
	if !*asJSON {
		return statusControl()
	}
	out, err := json.MarshalIndent(takeStatus(), "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// takeStatus collects the snapshot. A stopped control plane is not an error:
// the projects keep their ports and can be started from here.
func takeStatus() statusSnapshot {
	snap := statusSnapshot{
		Version:  versionNumber(),
		Control:  controlSnapshot{Log: controlLogPath()},
		Projects: []projectSnapshot{}, // [] not null: the app decodes a list
	}
	controlURL := ""
	if st, ok := readControlState(); ok && isRunning(st) {
		controlURL = project.LocalhostURL(st.Bound)
		snap.Control.Running = true
		snap.Control.PID = st.PID
		snap.Control.Addr = st.Addr
		snap.Control.URL = controlURL
		snap.Control.Started = st.Started
	}

	projects, err := project.ListProjects()
	if err != nil {
		return snap
	}
	for _, p := range projects {
		ps := projectSnapshot{
			ID:      p.ID,
			Running: projectRunning(p),
			WebUI:   p.Ports.WebUI,
			Bus:     p.Ports.Bus,
			Log:     project.LogPath(p.ID),
		}
		switch {
		case controlURL != "" && p.Ports.WebUI != 0:
			ps.URL = strings.TrimSuffix(controlURL, "/") + "/p/" + p.ID + "/"
		case p.Ports.WebUI != 0:
			ps.URL = project.LocalhostURL(fmt.Sprintf("127.0.0.1:%d", p.Ports.WebUI))
		}
		snap.Projects = append(snap.Projects, ps)
	}
	return snap
}
