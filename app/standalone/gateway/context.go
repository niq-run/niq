package gateway

// ContextInfo tells the SPA which mode the shell is serving. In control mode
// no project is attached; the project/bus fields are filled only when the
// shell is describing an attached project.
type ContextInfo struct {
	Mode    string `json:"mode"`
	Project string `json:"project,omitempty"`
	BusPort int    `json:"bus_port,omitempty"`
}
