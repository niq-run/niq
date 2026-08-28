package worker

import "context"

// Tool defines a capability exposed by a worker.
// It is the shared type used for internal worker capability
// declarations (via worker.ready).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`         // JSON Schema
	Provider    string         `json:"provider,omitempty"` // worker that provides this tool
}

// ToolFunc is the runtime signature for executing a tool call.
// It receives the call context and a map of arguments, and returns
// a result string or an error. All tool handlers in niq conform
// to this signature regardless of which worker provides them.
type ToolFunc func(ctx context.Context, args map[string]any) (string, error)
