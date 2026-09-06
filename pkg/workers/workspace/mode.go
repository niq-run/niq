// Read/write mode: the workspace can be switched at runtime between the
// default read-write mode and a read-only mode. Tools stay registered and
// listening in both modes — a write operation arriving while read-only is
// answered with an explicit error, not a missing capability. The mode is a
// durable attribute: it travels in the worker's Snapshot.
package workspace

import (
	"errors"
	"log"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/baseworker"
)

// Mode names on the wire (mode.set payload / snapshot).
const (
	ModeNameReadWrite = "readwrite"
	ModeNameReadOnly  = "readonly"
)

// TypeModeSet is the mode-switch extension event.
const TypeModeSet event.EventType = "mode.set"

// mutatingTools are the tool names rejected while read-only. Everything else
// (read/ls/grep/find and the management extensions mount.add/mode.set) keeps
// working.
var mutatingTools = map[string]bool{"write": true, "edit": true, "bash": true}

// registerModeExtension declares the mode.set extension.
func (w *WorkspaceWorker) registerModeExtension() {
	w.Register(baseworker.Extension{
		Event:       TypeModeSet,
		Description: "Switch the workspace between readwrite (default) and readonly. In readonly mode write/edit/bash calls are rejected; the tools stay declared.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"mode": map[string]any{"type": "string", "enum": []any{ModeNameReadWrite, ModeNameReadOnly}, "description": "Target mode."},
		}, "required": []any{"mode"}},
	}, w.handleModeSet)
}

// handleModeSet switches the runtime mode. The capability set does not change
// (tools stay registered); the re-announcement refreshes peers' view, and the
// mode itself is a durable change.
func (w *WorkspaceWorker) handleModeSet(evt event.Event) {
	modeName, _ := evt.Payload["mode"].(string)

	var target Mode
	switch modeName {
	case ModeNameReadWrite:
		target = ModeFull
	case ModeNameReadOnly:
		target = ModeReadOnly
	default:
		w.ReplyFailed(evt.WorkerId, evt.RequestId,
			errors.New(`mode must be "readwrite" or "readonly"`).Error(), evt.TraceID)
		return
	}

	w.mu.Lock()
	w.mode = target
	w.mu.Unlock()
	w.AnnounceReady("workspace", nil)
	w.NotifyDurableChange()
	log.Printf("[workspace %s] mode set to %s", w.ID(), modeName)
	w.ReplyCompleted(evt.WorkerId, evt.RequestId, `{"mode":"`+modeName+`"}`, evt.TraceID)
}

// modeName renders the current mode for the snapshot.
func modeName(m Mode) string {
	if m == ModeReadOnly {
		return ModeNameReadOnly
	}
	return ModeNameReadWrite
}
