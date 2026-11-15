package host

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
)

// fakeBackend records the lifecycle calls the host makes, so a test can assert
// the host is a pure protocol front-end: it lifts control fields, forwards the
// rest, and never invents ids.
type fakeBackend struct {
	created   []worker.WorkerSpec
	resolved  string
	suspended []string
	resumed   []string
	restarted []string
	destroyed []string
	createErr error
	protected map[string]bool
}

func (b *fakeBackend) Protected(id string) bool { return b.protected[id] }

func (b *fakeBackend) Create(_ context.Context, spec worker.WorkerSpec) (worker.WorkerInfo, error) {
	b.created = append(b.created, spec)
	if b.createErr != nil {
		return worker.WorkerInfo{}, b.createErr
	}
	id := b.resolved
	if id == "" {
		id = spec.ID
	}
	return worker.WorkerInfo{ID: id, Type: spec.Type, Placement: spec.Placement, State: "running"}, nil
}

func (b *fakeBackend) Suspend(_ context.Context, id string) error {
	b.suspended = append(b.suspended, id)
	return nil
}

func (b *fakeBackend) Resume(_ context.Context, id string) error {
	b.resumed = append(b.resumed, id)
	return nil
}

func (b *fakeBackend) Restart(_ context.Context, id string) error {
	b.restarted = append(b.restarted, id)
	return nil
}

func (b *fakeBackend) Destroy(_ context.Context, id string) error {
	b.destroyed = append(b.destroyed, id)
	return nil
}

func (b *fakeBackend) Get(string) (worker.WorkerInfo, bool, error) {
	return worker.WorkerInfo{}, false, nil
}
func (b *fakeBackend) List() []worker.WorkerInfo { return nil }

// call dispatches one host tool and returns its completed payload, or the
// failure message.
func call(t *testing.T, w *HostWorker, ch *recordingChannel, typ event.EventType, args map[string]any) (event.Event, bool) {
	t.Helper()
	before := len(ch.sent)
	evt := event.New(typ, "reason", args)
	evt.RequestId = "req-1"
	if !w.DispatchExtension(evt) {
		t.Fatalf("%s is not served by host", typ)
	}
	// A tool may emit more than its reply (a destroy parked on an approval
	// sends the approval.request first); the reply is always last.
	if len(ch.sent) <= before {
		t.Fatalf("%s produced no events", typ)
	}
	last := ch.sent[len(ch.sent)-1]
	return last, last.Type == event.TypeRequestCompleted
}

// TestSpawnLiftsPlacementAndLaunchFields verifies the placement/launch arguments
// become typed spec fields and never reach the builder as construction params,
// exactly like the display metadata they sit next to.
func TestSpawnLiftsPlacementAndLaunchFields(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{}
	w := New(Config{ID: "host", Bus: ch, Engine: be})

	reply, ok := call(t, w, ch, TypeSpawn, map[string]any{
		"type":      "mcp",
		"id":        "fs",
		"placement": "managed-process",
		"command":   []any{"npx", "-y", "server-filesystem"},
		"TOKEN":     "env:TOKEN",
		"cwd":       "/tmp",
		"label":     "文件系统",
		"servers":   []any{},
	})
	if !ok {
		t.Fatalf("spawn failed: %+v", reply.Payload)
	}
	if len(be.created) != 1 {
		t.Fatalf("backend saw %d creates", len(be.created))
	}
	spec := be.created[0]
	if spec.Placement != worker.PlacementManagedProcess {
		t.Fatalf("placement = %q", spec.Placement)
	}
	if len(spec.Command) != 3 || spec.Command[0] != "npx" {
		t.Fatalf("command = %#v", spec.Command)
	}
	if spec.Params["TOKEN"] != "env:TOKEN" || spec.Cwd != "/tmp" {
		t.Fatalf("params/cwd = %#v %q", spec.Params, spec.Cwd)
	}
	if spec.Label != "文件系统" {
		t.Fatalf("label = %q, want the display name", spec.Label)
	}
	for _, k := range []string{"type", "id", "placement", "command", "cwd", "label"} {
		if _, ok := spec.Params[k]; ok {
			t.Fatalf("%q leaked into the construction params: %+v", k, spec.Params)
		}
	}
	if _, ok := spec.Params["servers"]; !ok {
		t.Fatalf("a type-specific param was dropped: %+v", spec.Params)
	}
	if reply.Payload["placement"] != string(worker.PlacementManagedProcess) {
		t.Fatalf("reply = %+v", reply.Payload)
	}
}

// TestSpawnRepliesWithResolvedID verifies the reply carries the id the backend
// resolved (a derived id), not the empty one the caller passed.
func TestSpawnRepliesWithResolvedID(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{resolved: "ws-derived"}
	w := New(Config{ID: "host", Bus: ch, Engine: be})

	reply, ok := call(t, w, ch, TypeSpawn, map[string]any{"type": "workspace", "path": "/tmp/x"})
	if !ok {
		t.Fatalf("spawn failed: %+v", reply.Payload)
	}
	if reply.Payload["worker_id"] != "ws-derived" {
		t.Fatalf("worker_id = %v, want the resolved id", reply.Payload["worker_id"])
	}
}

// TestSpawnFailureIsReported verifies a backend error becomes request.failed
// (the caller must not see a successful spawn that created nothing).
func TestSpawnFailureIsReported(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{createErr: errors.New("boom")}
	w := New(Config{ID: "host", Bus: ch, Engine: be})

	reply, ok := call(t, w, ch, TypeSpawn, map[string]any{"type": "reason", "id": "r"})
	if ok {
		t.Fatalf("spawn reported success: %+v", reply.Payload)
	}
	if msg, _ := reply.Payload["error"].(string); !strings.Contains(msg, "boom") {
		t.Fatalf("error = %q", msg)
	}
}

// TestLifecycleToolsForwardIDs verifies suspend/resume/restart/destroy reach the
// backend with the requested id and reply with their status.
func TestLifecycleToolsForwardIDs(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{}
	w := New(Config{ID: "host", Bus: ch, Engine: be})

	if _, ok := call(t, w, ch, TypeSuspend, map[string]any{"worker_id": "w1"}); !ok {
		t.Fatal("suspend failed")
	}
	if _, ok := call(t, w, ch, TypeResume, map[string]any{"worker_id": "w1"}); !ok {
		t.Fatal("resume failed")
	}
	if _, ok := call(t, w, ch, TypeRestart, map[string]any{"worker_id": "w1"}); !ok {
		t.Fatal("restart failed")
	}
	reply, ok := call(t, w, ch, TypeDestroy, map[string]any{"worker_id": "w1"})
	if !ok {
		t.Fatal("destroy failed")
	}
	if reply.Payload["status"] != "destroyed" {
		t.Fatalf("destroy reply = %+v", reply.Payload)
	}
	for _, got := range [][]string{be.suspended, be.resumed, be.restarted, be.destroyed} {
		if len(got) != 1 || got[0] != "w1" {
			t.Fatalf("backend calls = %+v", got)
		}
	}
}

// TestDestroyParksOnApproval verifies the EXECUTOR raises the approval: the
// caller gets pending_approval immediately, the approver receives an
// approval.request naming worker.destroy, and nothing is destroyed yet.
func TestDestroyParksOnApproval(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{}
	w := New(Config{
		ID: "host", Bus: ch, Engine: be,
		Params: map[string]any{"destroy_approver": "webui-hiw"},
	})

	reply, ok := call(t, w, ch, TypeDestroy, map[string]any{"worker_id": "w1"})
	if !ok {
		t.Fatalf("destroy failed: %+v", reply.Payload)
	}
	if reply.Payload["status"] != "pending_approval" {
		t.Fatalf("status = %v, want pending_approval", reply.Payload["status"])
	}
	approvalID, _ := reply.Payload["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("no approval id returned")
	}
	if len(be.destroyed) != 0 {
		t.Fatal("destroyed before the approval decision")
	}

	// The approval request went to the configured approver, with the action the
	// UI labels the queue entry with.
	var req event.Event
	for i := len(ch.sent) - 1; i >= 0; i-- {
		if ch.sent[i].Type == event.TypeApprovalRequest {
			req = ch.sent[i]
			break
		}
	}
	if req.Type != event.TypeApprovalRequest {
		t.Fatalf("no approval.request was sent: %+v", ch.sent)
	}
	if req.Payload["action"] != "worker.destroy" || req.Payload["worker_id"] != "w1" {
		t.Fatalf("approval payload = %+v", req.Payload)
	}
	if req.RequestId != approvalID {
		t.Fatalf("approval request id %q != returned %q", req.RequestId, approvalID)
	}

	// Denying leaves the worker alone.
	decision := event.New(event.TypeApprovalDecision, "webui-hiw", map[string]any{"approved": false, "note": "not now"})
	decision.RequestId = approvalID
	w.DispatchExtension(decision)
	if len(be.destroyed) != 0 {
		t.Fatal("a denied destroy was applied")
	}
}

// TestDestroyApprovalApplies verifies an approved decision destroys the worker
// and answers the original caller on the same approval id.
func TestDestroyApprovalApplies(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{}
	w := New(Config{
		ID: "host", Bus: ch, Engine: be,
		Params: map[string]any{"destroy_approval": true},
	})

	reply, _ := call(t, w, ch, TypeDestroy, map[string]any{"worker_id": "w1"})
	approvalID, _ := reply.Payload["approval_id"].(string)

	decision := event.New(event.TypeApprovalDecision, "webui-hiw", map[string]any{"approved": true})
	decision.RequestId = approvalID
	w.DispatchExtension(decision)

	// Destroy runs on its own goroutine; wait for the call to land.
	for i := 0; i < 100 && len(be.destroyed) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if len(be.destroyed) != 1 || be.destroyed[0] != "w1" {
		t.Fatalf("destroyed = %+v", be.destroyed)
	}
}

// TestPendingDestroySurvivesSnapshot verifies a parked destroy is durable: a
// restart that replays the snapshot can still apply the human's decision.
func TestPendingDestroySurvivesSnapshot(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{}
	w := New(Config{ID: "host", Bus: ch, Engine: be, Params: map[string]any{"destroy_approver": "webui-hiw"}})
	reply, _ := call(t, w, ch, TypeDestroy, map[string]any{"worker_id": "w1"})
	approvalID, _ := reply.Payload["approval_id"].(string)

	state, err := w.Snapshot()
	if err != nil || len(state) == 0 {
		t.Fatalf("Snapshot = %q, %v", state, err)
	}

	// A fresh host restoring that state must act on the decision.
	ch2 := &recordingChannel{}
	be2 := &fakeBackend{}
	w2 := New(Config{ID: "host", Bus: ch2, Engine: be2})
	if err := w2.Restore(state); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	decision := event.New(event.TypeApprovalDecision, "webui-hiw", map[string]any{"approved": true})
	decision.RequestId = approvalID
	w2.DispatchExtension(decision)

	for i := 0; i < 100 && len(be2.destroyed) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if len(be2.destroyed) != 1 || be2.destroyed[0] != "w1" {
		t.Fatalf("restored host did not apply the approved destroy: %+v", be2.destroyed)
	}
}

// TestDestroyRefusesProtectedBeforeApproval pins the ordering: a protected id
// is refused outright, before any approval is raised. Parking an approval for
// the host would ask a human to approve something that can never be applied.
func TestDestroyRefusesProtectedBeforeApproval(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{protected: map[string]bool{"host": true}}
	w := New(Config{
		ID: "host", Bus: ch, Engine: be,
		Params: map[string]any{"destroy_approver": "webui-hiw"},
	})

	reply, ok := call(t, w, ch, TypeDestroy, map[string]any{"worker_id": "host"})
	if ok {
		t.Fatalf("destroy of a protected worker reported success: %+v", reply.Payload)
	}
	for _, evt := range ch.sent {
		if evt.Type == event.TypeApprovalRequest {
			t.Fatalf("an approval was raised for a protected worker: %+v", evt.Payload)
		}
	}
	if len(be.destroyed) != 0 {
		t.Fatal("backend was asked to destroy a protected worker")
	}
}
