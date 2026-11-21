// Package contract holds the shapes shared by every way of looking at a niq
// instance, and the HTTP client that fetches them.
//
// The same structs are persisted (project.json), served (/api/projects) and
// decoded by clients, so there is exactly one definition of a project view.
// This package depends on nothing but the standard library: it is safe for any
// UI to import, and it must stay that way.
package contract

// Ports records the addresses assigned to a project instance: its event-bus
// (HTTP transport) port and its WebUI port. Zero means "not allocated".
type Ports struct {
	Bus   int `json:"bus,omitempty"`
	WebUI int `json:"webui,omitempty"`
}

// Project is the project-level metadata stored in project.json.
type Project struct {
	SchemaVersion int    `json:"schema_version,omitempty"`
	ID            string `json:"id"`
	CreatedAt     string `json:"created_at,omitempty"`
	Ports         Ports  `json:"ports,omitempty"`
	UploadDir     string `json:"upload_dir,omitempty"`
}

// View is a project definition plus its live run state and its directory on
// this instance, as listed by the gateway. Dir is resolved server-side so a
// client never has to know how paths are laid out.
type View struct {
	Project
	Dir     string `json:"dir,omitempty"`
	Running bool   `json:"running"`
}

// Instance is the result of starting or restarting a project.
type Instance struct {
	ID        string `json:"project"`
	WebUIAddr string `json:"-"`
	WebUIPort int    `json:"webui_port"`
	BusPort   int    `json:"bus_port,omitempty"`
	Already   bool   `json:"already,omitempty"`
}
