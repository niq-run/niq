package gateway

// ContextInfo is the reply to GET /api/context: which mode a shell is serving.
// This service only ever encodes "control" — no project is attached to the
// gateway — so Project and BusPort stay empty here; a project's own API answers
// the same question in project/api with its own shape.
type ContextInfo struct {
	Mode    string `json:"mode"`
	Project string `json:"project,omitempty"`
	BusPort int    `json:"bus_port,omitempty"`
}
