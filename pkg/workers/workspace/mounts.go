// Mount management: the runtime mount set of a workspace worker, exposed
// to the bus through the extension mechanism.
//
// Mounts follow the same pattern as the reason worker's provider events:
// dedicated event types (mount.add / mount.list) handled by extensions,
// validated payloads, and a durable-change notification on success so the
// expanded mount set survives a restart via Snapshot/Restore.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/baseworker"
)

// The mount management extensions use their own event types rather than
// multiplexing inside worker.update/worker.query — these are
// workspace-worker specific, so they live here, not in core/event.
const (
	TypeMountAdd    event.EventType = "mount.add"
	TypeMountList   event.EventType = "mount.list"
	TypeMountRemove event.EventType = "mount.remove"
)

// MountManager probes the backend for mount listing and mutation (the
// backend is held as `any`). Implemented by wsbackend.EmbeddedBackend.
type MountManager interface {
	Mounts() []string
	AddMount(path string) (string, error)
	RemoveMount(path string) (bool, error)
	ReplaceMounts(paths []string) error
}

// registerMountExtensions declares the mount management extensions served
// by their own event types, announced to peers via AnnounceReady. They are
// registered unconditionally: without a MountManager backend the handlers
// reject requests with a clear error instead of silently disappearing.
func (w *WorkspaceWorker) registerMountExtensions() {
	w.Register(baseworker.Extension{
		Event:       TypeMountAdd,
		Description: "Mount an additional directory into the workspace. The path must exist and be a directory. Requests from the workspace's approver apply directly; requests from any other worker wait for the approver's consent (approval.request/approval.decision). Relative tool paths keep resolving against the primary (first) mount; absolute paths inside any mount are accepted.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Absolute directory path to mount (~ expansion allowed). Must exist."},
		}, "required": []any{"path"}},
	}, w.handleMountAdd)

	w.Register(baseworker.Extension{
		Event:       TypeMountList,
		Description: "List the workspace's mounted directories. The first one is the primary mount.",
	}, w.handleMountList)

	w.Register(baseworker.Extension{
		Event:       TypeMountRemove,
		Description: "Unmount a mounted directory. Relative tool paths resolve against the primary (first) mount, so removing it promotes the next one. The last remaining mount cannot be removed.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Mounted directory path to remove (~ expansion allowed)."},
		}, "required": []any{"path"}},
	}, w.handleMountRemove)
}

// handleMountAdd serves a mount.add request: it appends the directory to the
// backend's mount set. The approver itself may extend the boundary directly;
// a request from any other worker is parked behind an approval — the approver
// must consent before the boundary expands. Either way a successful add is a
// durable change (NotifyDurableChange). Without a configured approver every
// request applies directly.
func (w *WorkspaceWorker) handleMountAdd(evt event.Event) {
	path, _ := evt.Payload["path"].(string)
	mm, err := w.mountManager()
	if err == nil && path == "" {
		err = errors.New("path is required")
	}
	if err != nil {
		log.Printf("[workspace %s] mount.add %q failed: %v", w.ID(), path, err)
		w.ReplyFailed(evt.WorkerId, evt.RequestId, err.Error(), evt.TraceID)
		return
	}

	// Approval gating: the approver's own request is the consent; any other
	// worker's request waits for it.
	if w.approver != "" && evt.WorkerId != w.approver {
		w.requestMountApproval(evt, path, mm.Mounts())
		return
	}

	added, err := mm.AddMount(path)
	if err != nil {
		log.Printf("[workspace %s] mount.add %q failed: %v", w.ID(), path, err)
		w.ReplyFailed(evt.WorkerId, evt.RequestId, err.Error(), evt.TraceID)
		return
	}
	log.Printf("[workspace %s] mounted %s", w.ID(), added)
	w.NotifyDurableChange()
	w.ReplyCompleted(evt.WorkerId, evt.RequestId, mountSnapshotJSON(added, mm), evt.TraceID)
}

// requestMountApproval parks a non-approver's mount.add behind the approver's
// decision. The request is NOT answered here — handleApprovalDecision applies
// the mount and replies to the requester when the verdict arrives. As with
// tool escapes, the approved object is the path itself (existence is not a
// precondition — the directory may be created later). mounts is the boundary
// being expanded, carried in the payload for the approver's context.
func (w *WorkspaceWorker) requestMountApproval(evt event.Event, path string, mounts []string) {
	_, existsErr := os.Stat(path)
	appr := event.New(event.TypeApprovalRequest, w.ID(), jsonSafe(map[string]any{
		"origin":       evt.WorkerId, // who set this in motion (reason worker / human UI)
		"requested_by": w.ID(),      // the worker sending this approval request
		"action":       "mount.add",
		"path":         path,
		"exists":       existsErr == nil,
		"mounts":       mounts, // the boundary being expanded, at request time
	}))
	appr.RequestId = appr.ID
	appr.TraceID = evt.TraceID
	if err := w.Channel.Send(context.Background(), appr, w.approver); err != nil {
		log.Printf("[workspace %s] approval request to %s: %v", w.ID(), w.approver, err)
		w.ReplyFailed(evt.WorkerId, evt.RequestId, "approval request failed: "+err.Error(), evt.TraceID)
		return
	}
	w.pendingMu.Lock()
	w.pendingApprovals[appr.ID] = &pendingApproval{
		ApprovalID: appr.ID,
		Path:       path,
		CallerID:   evt.WorkerId,
		CallID:     evt.RequestId,
		TraceID:    evt.TraceID,
	}
	w.pendingMu.Unlock()
	w.NotifyDurableChange()
	log.Printf("[workspace %s] approval requested from %s: mount %s (requested by %s, parked)",
		w.ID(), w.approver, path, evt.WorkerId)
}

// handleMountRemove serves a mount.remove request: it detaches the given
// directory from the mount set. Removal narrows the boundary, so it is not
// approval-gated. A successful remove is a durable change.
func (w *WorkspaceWorker) handleMountRemove(evt event.Event) {
	path, _ := evt.Payload["path"].(string)
	mm, err := w.mountManager()
	if err == nil && path == "" {
		err = errors.New("path is required")
	}
	if err == nil {
		var removed bool
		removed, err = mm.RemoveMount(path)
		if err == nil {
			if removed {
				log.Printf("[workspace %s] unmounted %s", w.ID(), path)
				w.NotifyDurableChange()
			}
			w.ReplyCompleted(evt.WorkerId, evt.RequestId, mountSnapshotJSON("", mm), evt.TraceID)
			return
		}
	}
	log.Printf("[workspace %s] mount.remove %q failed: %v", w.ID(), path, err)
	w.ReplyFailed(evt.WorkerId, evt.RequestId, err.Error(), evt.TraceID)
}

// handleMountList serves a mount.list request: the reply's "result" carries
// the mounted directories as JSON — the same {"result"} shape every tool
// reply on the bus uses.
func (w *WorkspaceWorker) handleMountList(evt event.Event) {
	mm, err := w.mountManager()
	if err != nil {
		w.ReplyFailedTransient(evt.WorkerId, evt.RequestId, err.Error(), evt.TraceID)
		return
	}
	// Read-only query: its result is transient, not durable history.
	w.ReplyCompletedTransient(evt.WorkerId, evt.RequestId, mountSnapshotJSON("", mm), evt.TraceID)
}

// mountManager probes the backend for the MountManager interface.
func (w *WorkspaceWorker) mountManager() (MountManager, error) {
	mm, ok := w.backend.(MountManager)
	if !ok {
		return nil, errors.New("backend does not support mounts")
	}
	return mm, nil
}

// mountSnapshotJSON renders the mount list (plus the just-added path when
// non-empty) as the reply result. Only string values and []string travel in
// the snapshot, so marshalling cannot fail.
func mountSnapshotJSON(added string, mm MountManager) string {
	mounts := mm.Mounts()
	snapshot := map[string]any{"mounts": mounts}
	if added != "" {
		snapshot["added"] = added
	}
	if len(mounts) > 0 {
		snapshot["primary"] = mounts[0]
	}
	b, _ := json.Marshal(snapshot)
	return string(b)
}
