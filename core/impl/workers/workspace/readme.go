package workspace

import (
	"fmt"
	"sort"
	"strings"
)

// registerReadme declares the generic worker.readme capability: the guide
// describes the tools this backend actually provides, the path boundary, and
// the current mounts/mode.
func (w *WorkspaceWorker) registerReadme() {
	w.RegisterReadme(
		"Read this worker's usage guide: its file and shell tools, path boundaries, "+
			"mount/mode controls, and current mounts.",
		w.readmeText, nil)
}

// readmeText is the guide plus the worker's live mounts, mode and tool set.
func (w *WorkspaceWorker) readmeText() string {
	var mounts []string
	if mm, ok := w.backend.(MountManager); ok {
		mounts = mm.Mounts()
	}
	w.mu.Lock()
	mode := modeName(w.mode)
	tools := make([]string, 0, len(w.handlers))
	for name := range w.handlers {
		tools = append(tools, name)
	}
	w.mu.Unlock()
	sort.Strings(tools)

	mountList := "(none)"
	primary := "(none)"
	if len(mounts) > 0 {
		mountList = strings.Join(mounts, ", ")
		primary = mounts[0]
	}
	approver := w.approver
	if approver == "" {
		approver = "none (boundary escapes fail immediately)"
	}
	toolList := "(none)"
	if len(tools) > 0 {
		toolList = strings.Join(tools, ", ")
	}

	return fmt.Sprintf(
		"%s\n\n## Current\n\n- mode: %s\n- mounts: %s\n- primary mount: %s\n- approver: %s\n- tools available: %s\n",
		workspaceReadme, mode, mountList, primary, approver, toolList)
}

// workspaceReadme is the agent-facing usage guide returned by worker.readme.
const workspaceReadme = `# workspace — files and shell

This worker is a filesystem and shell boundary: every path it touches must fall
inside one of its mounted directories. Use it to read, inspect and change the
project on disk, and to run commands there.

## Tools

File and search:
- read { path, offset?, limit? } — file content with line numbers.
- readImg { path } — an image file as a viewable image (png/jpeg/gif).
- write { path, content } — write a whole file, creating parents.
- edit { path, edits: [{old_text,new_text}] } — find-and-replace edits.
- ls { path? } — directory entries marked [file] / [dir].
- grep { pattern, path?, include?, exclude? } — recursive regex search.
- find { pattern, path? } — find paths by a name glob.
- bash { command, cwd?, timeout? } — run a shell command; returns exit code,
  stdout and stderr. Output is capped at 20KB (head + tail); a timeout kills the
  process and preserves the partial output.

Boundary and mode:
- mount.add { path } — mount an extra directory. A non-approver's request waits
  for approval; the approver's own request applies directly.
- mount.list {} — the current mount set (the first is the primary).
- mount.remove { path } — unmount; the last remaining mount cannot be removed.
- mode.set { mode: "readwrite" | "readonly" } — readonly rejects write/edit/bash
  while keeping every tool declared and listening.

## Path rules

- Relative paths resolve against the primary (first) mount.
- Absolute paths are accepted when they fall inside any mounted directory.
- A path that escapes every mount fails; with an approver configured it becomes
  an approval.request, and once approved that path is mounted and the call is
  retried.
- The tool set can vary with the backend: only tools this backend supports are
  announced.

## Notes

- A non-zero exit code from bash is a result, not a tool failure — read the exit
  code and stderr to decide what happened.
- mount.add / mount.remove / mode.set are durable: they survive a restart.`
