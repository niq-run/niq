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
// It receives the call context and a map of arguments, and returns a result
// or an error. All tool handlers in niq conform to this signature regardless
// of which worker provides them.
//
// The result is a neutral, JSON-serializable value — typically a string, or a
// map[string]any when the tool wants to return structured content (e.g. a
// `{content:[{type, mime, data}, …]}` payload the reason worker decodes into
// multimodal llm content). Workers never import the llm package or build
// llm.ContentBlock directly; they only shape a payload the bus serializes and
// the caller translates.
type ToolFunc func(ctx context.Context, args map[string]any) (any, error)
