package host

import (
	"context"
	"errors"
	"strings"
	"testing"

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
	patches   map[string]worker.WorkerPatch
	archived  map[string]bool
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

func (b *fakeBackend) Update(_ context.Context, id string, patch worker.WorkerPatch) (worker.WorkerInfo, error) {
	if b.patches == nil {
		b.patches = map[string]worker.WorkerPatch{}
	}
	b.patches[id] = patch
	return worker.WorkerInfo{ID: id, State: "running"}, nil
}

func (b *fakeBackend) Archive(_ context.Context, id string, archived bool) error {
	if b.archived == nil {
		b.archived = map[string]bool{}
	}
	b.archived[id] = archived
	return nil
}

func (b *fakeBackend) Get(string) (worker.WorkerInfo, bool, error) {
	return worker.WorkerInfo{}, false, nil
}
func (b *fakeBackend) List() []worker.WorkerInfo { return nil }
func (b *fakeBackend) Archived() []string        { return nil }

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
	// The reply is the last event a tool emits.
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

// TestDestroyRefusesProtected verifies a protected id is refused outright.
func TestDestroyRefusesProtected(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{protected: map[string]bool{"host": true}}
	w := New(Config{ID: "host", Bus: ch, Engine: be})

	reply, ok := call(t, w, ch, TypeDestroy, map[string]any{"worker_id": "host"})
	if ok {
		t.Fatalf("destroy of a protected worker reported success: %+v", reply.Payload)
	}
	if len(be.destroyed) != 0 {
		t.Fatal("backend was asked to destroy a protected worker")
	}
}

// TestUpdateSendsOnlyGivenFields verifies the host distinguishes "omitted" from
// "cleared": a missing key must leave that part of the declaration alone, while
// an empty list / object clears it.
func TestUpdateSendsOnlyGivenFields(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{}
	w := New(Config{ID: "host", Bus: ch, Engine: be})

	if _, ok := call(t, w, ch, TypeUpdate, map[string]any{
		"worker_id": "r1",
		"label":     "值班",
		"tags":      []any{"ops"},
	}); !ok {
		t.Fatal("update failed")
	}
	patch := be.patches["r1"]
	if patch.Label == nil || *patch.Label != "值班" {
		t.Fatalf("label = %v", patch.Label)
	}
	if len(patch.Tags) != 1 || patch.Tags[0] != "ops" {
		t.Fatalf("tags = %+v", patch.Tags)
	}
	if patch.Description != nil || patch.Params != nil {
		t.Fatalf("omitted fields were sent: %+v", patch)
	}

	// Present-but-empty clears.
	if _, ok := call(t, w, ch, TypeUpdate, map[string]any{
		"worker_id": "r1",
		"tags":      []any{},
		"params":    map[string]any{},
	}); !ok {
		t.Fatal("update (clear) failed")
	}
	patch = be.patches["r1"]
	if patch.Tags == nil || len(patch.Tags) != 0 {
		t.Fatalf("tags = %+v, want an empty (non-nil) list", patch.Tags)
	}
	if patch.Params == nil || len(patch.Params) != 0 {
		t.Fatalf("params = %+v, want an empty (non-nil) map", patch.Params)
	}

	// update takes a worker id.
	if _, ok := call(t, w, ch, TypeUpdate, map[string]any{"label": "x"}); ok {
		t.Fatal("update without worker_id succeeded")
	}
}

// TestArchiveForwardsIntent verifies archive is one backend call carrying the
// direction, and that a protected worker is refused before the backend sees it.
func TestArchiveForwardsIntent(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{protected: map[string]bool{"host": true}}
	w := New(Config{ID: "host", Bus: ch, Engine: be})

	if _, ok := call(t, w, ch, TypeArchive, map[string]any{"worker_id": "r1", "archived": true}); !ok {
		t.Fatal("archive failed")
	}
	if !be.archived["r1"] {
		t.Fatalf("archived = %v, want true", be.archived)
	}
	if _, ok := call(t, w, ch, TypeArchive, map[string]any{"worker_id": "r1", "archived": false}); !ok {
		t.Fatal("unarchive failed")
	}
	if be.archived["r1"] {
		t.Fatal("unarchive did not forward archived=false")
	}

	if _, ok := call(t, w, ch, TypeArchive, map[string]any{"worker_id": "host", "archived": true}); ok {
		t.Fatal("archiving a protected worker succeeded")
	}
	if _, ok := be.archived["host"]; ok {
		t.Fatal("protected worker reached the backend")
	}
}

// TestSpawnAcceptsNestedParams verifies the tool takes construction params either
// flat (an agent) or under a `params` object (a UI form) — the flattening is the
// tool's own input boundary, so no caller has to know spawn's shape.
func TestSpawnAcceptsNestedParams(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{}
	w := New(Config{ID: "host", Bus: ch, Engine: be})

	if _, ok := call(t, w, ch, TypeSpawn, map[string]any{
		"type":   "reason",
		"id":     "r1",
		"params": map[string]any{"goal": "watch the queue", "model": "m"},
	}); !ok {
		t.Fatal("spawn with nested params failed")
	}
	spec := be.created[0]
	if spec.Params["goal"] != "watch the queue" || spec.Params["model"] != "m" {
		t.Fatalf("params = %+v, want the nested object flattened", spec.Params)
	}
	// Control fields must not survive inside params.
	if _, ok := spec.Params["params"]; ok {
		t.Fatalf("params kept the wrapper: %+v", spec.Params)
	}
}

// TestSpawnRejectsParamCollidingWithLaunchField verifies a param cannot shadow a
// control field: `id`/`placement`/`command` decide how the worker runs.
func TestSpawnRejectsParamCollidingWithLaunchField(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{}
	w := New(Config{ID: "host", Bus: ch, Engine: be})

	for _, tc := range []map[string]any{
		{"type": "reason", "id": "r1", "params": map[string]any{"id": "other"}},
		{"type": "mcp", "id": "e1", "placement": "managed-process", "params": map[string]any{"placement": "in-process"}},
		{"type": "reason", "params": map[string]any{"type": "workspace"}},
	} {
		if _, ok := call(t, w, ch, TypeSpawn, tc); ok {
			t.Fatalf("collision accepted: %v", tc)
		}
	}
	if len(be.created) != 0 {
		t.Fatalf("a rejected spawn reached the backend: %+v", be.created)
	}
	if _, ok := call(t, w, ch, TypeSpawn, map[string]any{"type": "reason", "params": "nope"}); ok {
		t.Fatal("a non-object params value was accepted")
	}
}

// TestUpdateACLGoesToItsOwnPort verifies acl.update is routed to the identity
// administrator, not the lifecycle backend: grants are bus identity data.
func TestUpdateACLGoesToItsOwnPort(t *testing.T) {
	ch := &recordingChannel{}
	be := &fakeBackend{}
	acl := &fakeACL{}
	w := New(Config{ID: "host", Bus: ch, Engine: be, ACL: acl})

	if _, ok := call(t, w, ch, TypeUpdateACL, map[string]any{
		"worker_id":       "r1",
		"publish_allow":   []any{map[string]any{"type": "worker.ready"}, map[string]any{"type": "request.*", "target": "webui-hiw"}},
		"subscribe_allow": []any{map[string]any{"type": "worker.discover", "source": "*"}},
	}); !ok {
		t.Fatal("acl.update failed")
	}
	if acl.id != "r1" {
		t.Fatalf("id = %q", acl.id)
	}
	if len(acl.publish) != 2 || acl.publish[1].Type != "request.*" || acl.publish[1].Target != "webui-hiw" {
		t.Fatalf("publish = %+v", acl.publish)
	}
	if len(acl.subscribe) != 1 || acl.subscribe[0].SourceID != "*" {
		t.Fatalf("subscribe = %+v", acl.subscribe)
	}
	if len(be.created)+len(be.suspended)+len(be.restarted)+len(be.destroyed) != 0 || len(be.patches) != 0 {
		t.Fatal("acl.update reached the lifecycle backend")
	}

	// Both lists are replaced wholesale, so an omitted list means "no grants".
	if _, ok := call(t, w, ch, TypeUpdateACL, map[string]any{"worker_id": "r1"}); !ok {
		t.Fatal("acl.update with no lists failed")
	}
	if acl.publish == nil || len(acl.publish) != 0 || acl.subscribe == nil || len(acl.subscribe) != 0 {
		t.Fatalf("omitted lists = %+v / %+v, want empty (not nil)", acl.publish, acl.subscribe)
	}

	if _, ok := call(t, w, ch, TypeUpdateACL, map[string]any{"publish_allow": []any{}}); ok {
		t.Fatal("acl.update without worker_id succeeded")
	}
}

// TestUpdateACLDisabledWithoutPort verifies the tool is absent when no identity
// administrator is configured (e.g. a bus-less test host).
func TestUpdateACLDisabledWithoutPort(t *testing.T) {
	ch := &recordingChannel{}
	w := New(Config{ID: "host", Bus: ch, Engine: &fakeBackend{}})
	if w.DispatchExtension(event.New(TypeUpdateACL, "reason", map[string]any{"worker_id": "r1"})) {
		t.Fatal("acl.update served without an ACL administrator")
	}
}

// fakeACL records the grants the host forwards.
type fakeACL struct {
	id        string
	publish   []event.PublishPattern
	subscribe []event.EventPattern
}

func (f *fakeACL) UpdateACL(id string, publish []event.PublishPattern, subscribe []event.EventPattern) error {
	f.id, f.publish, f.subscribe = id, publish, subscribe
	if f.publish == nil {
		f.publish = []event.PublishPattern{}
	}
	if f.subscribe == nil {
		f.subscribe = []event.EventPattern{}
	}
	return nil
}
