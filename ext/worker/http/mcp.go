package http

import (
	"context"
	"fmt"
	"log"

	"github.com/niq-run/niq/core/worker"
)

// mcpConn represents a connected MCP server and its discovered tools.
type mcpConn struct {
	name  string
	tools []worker.Tool
}

// connectMCP reads the MCP config file and connects to each server.
// TODO: read JSON config, MCP handshake, tool discovery.
func (w *Worker) connectMCP(_ context.Context) {
	if w.mcpConfig == "" {
		return
	}
	log.Printf("[http worker %s] MCP config present (%s) — handshake not yet implemented", w.ID(), w.mcpConfig)
}

// tryMCPTool checks if name matches a tool from any connected MCP server.
// TODO: proxy the call to the MCP server and return the result.
func (w *Worker) tryMCPTool(name string, _ map[string]any) (bool, string, error) {
	for _, srv := range w.mcpServers {
		for _, t := range srv.tools {
			if t.Name == name {
				return true, "", fmt.Errorf("mcp tool %s: proxy not yet implemented", name)
			}
		}
	}
	return false, "", nil
}
