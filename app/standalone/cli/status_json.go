// `niq status --json` is the same snapshot the report draws, for callers that
// render their own UI — the macOS app needs the gateway's state,
// the address that actually reaches it, and one entry per project with the
// things that can be done to it.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"

	"github.com/niq-run/niq/app/standalone/contract"
	"github.com/niq-run/niq/app/standalone/launcher"
	"github.com/niq-run/niq/app/standalone/store"
)

type statusSnapshot struct {
	Version  string            `json:"version"`
	Gateway  gatewaySnapshot   `json:"gateway"`
	Projects []projectSnapshot `json:"projects"`
}

type gatewaySnapshot struct {
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
	asIDs := fs.Bool("ids", false, "Print project ids, one per line")
	fs.Usage = func() {
		fmt.Println(bold("niq status") + dim(" — the gateway and every project instance"))
		fmt.Println()
		fmt.Println(bold("  --json") + dim("  the same facts, machine-readable"))
	}
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err)
	}
	if *asIDs {
		for _, p := range takeStatus().Projects {
			fmt.Println(p.ID)
		}
		return nil
	}
	if !*asJSON {
		return printStatus()
	}
	out, err := json.MarshalIndent(takeStatus(), "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// takeStatus collects the snapshot. A stopped gateway is not an error:
// the projects keep their ports and can be started from here.
func takeStatus() statusSnapshot {
	snap := statusSnapshot{
		Version:  versionNumber(),
		Gateway:  gatewaySnapshot{Log: shellLogPath()},
		Projects: []projectSnapshot{}, // [] not null: the app decodes a list
	}
	gatewayURL := ""
	if st, ok := readShellState(); ok && isRunning(st) {
		gatewayURL = store.LocalhostURL(st.Bound)
		snap.Gateway.Running = true
		snap.Gateway.PID = st.PID
		snap.Gateway.Addr = st.Addr
		snap.Gateway.URL = gatewayURL
		snap.Gateway.Started = st.Started
	}

	views, err := launcher.List()
	if err != nil {
		return snap
	}
	for _, v := range views {
		ps := projectSnapshot{
			ID:      v.ID,
			Running: v.Running,
			WebUI:   v.Ports.WebUI,
			Bus:     v.Ports.Bus,
			Log:     store.LogPath(v.ID),
		}
		switch {
		case gatewayURL != "" && v.Ports.WebUI != 0:
			ps.URL = contract.ProjectURL(gatewayURL, v.ID)
		case v.Ports.WebUI != 0:
			ps.URL = store.LocalhostURL(fmt.Sprintf("127.0.0.1:%d", v.Ports.WebUI))
		}
		snap.Projects = append(snap.Projects, ps)
	}
	return snap
}
