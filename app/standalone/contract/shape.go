// Package contract holds what crosses the gateway's project API: the wire
// shapes a client decodes and the client that does the decoding.
//
// It is deliberately **not** the disk shape. project.json is store.Project; the
// launcher maps that to a View, so a field added to the file stays private until
// someone decides clients should see it. That mapping is also why this package
// imports nothing but the standard library: any interface, including one on
// another machine, may import it and must not inherit the form.
package contract

// Ports is the address pair of a project instance: its event-bus (HTTP
// transport) port and its WebUI port. Zero means "not allocated". It is the one
// value the file and the wire spell the same way, so it lives here and
// store.Project embeds it instead of redeclaring it.
type Ports struct {
	Bus   int `json:"bus,omitempty"`
	WebUI int `json:"webui,omitempty"`
}

// View is one project as /api/projects lists it: what a client needs, plus the
// run state and the directory resolved by the gateway — so no client has to
// know how paths are laid out, or how a project is probed.
type View struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at,omitempty"`
	Ports     Ports  `json:"ports,omitempty"`
	Dir       string `json:"dir,omitempty"`
	Running   bool   `json:"running"`
}

// Instance is the result of starting or restarting a project: the ports it
// actually bound, and whether it was already up.
type Instance struct {
	ID        string `json:"project"`
	WebUIPort int    `json:"webui_port"`
	BusPort   int    `json:"bus_port,omitempty"`
	Already   bool   `json:"already,omitempty"`
}
