package niw

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// memCh is a minimal in-memory WorkerSideChannel for exercising the bridge in
// isolation. It records directed sends and broadcasts, and exposes a channel
// of events routed to the worker via Receive.
type memCh struct {
	id         string
	sends      []event.Event
	sendTargs  [][]string
	broadcasts []event.Event
	in         chan event.Event
	mu         sync.Mutex
}

func newMemCh(id string) *memCh {
	return &memCh{id: id, in: make(chan event.Event, 16)}
}

func (m *memCh) ID() string                                              { return m.id }
func (m *memCh) Close() error                                            { return nil }
func (m *memCh) Receive(ctx context.Context) (<-chan event.Event, error) { return m.in, nil }

func (m *memCh) Send(ctx context.Context, evt event.Event, targets ...string) error {
	m.mu.Lock()
	m.sends = append(m.sends, evt)
	m.sendTargs = append(m.sendTargs, targets)
	m.mu.Unlock()
	return nil
}

func (m *memCh) Broadcast(ctx context.Context, evt event.Event) error {
	m.mu.Lock()
	m.broadcasts = append(m.broadcasts, evt)
	m.mu.Unlock()
	return nil
}

// testWorker builds a NIW bound to memCh channels without dialing a real bus.
// startRemote sets w.remote so the forward/deliver methods have a remote to use.
func testWorker(localSeed, remoteSeed string) (*Worker, *memCh, *memCh) {
	local := newMemCh("niw-test")
	remote := newMemCh("remoteid")
	w := New(Config{ID: "niw-test", Bus: local, Recipient: localSeed,
		RemoteURL: "http://127.0.0.1:1", RemoteWorkerID: "remoteid"})
	w.setRemote(remote)
	if remoteSeed != "" {
		w.setRemoteRecipient(remoteSeed)
	}
	return w, local, remote
}

// TestForwardToRemoteDirected verifies a local worker.input is DIRECTED (not
// broadcast) at the far-side recipient, and dropped when the far side hasn't set
// one.
func TestForwardToRemoteDirected(t *testing.T) {
	w, _, remote := testWorker("", "b-worker")

	evt := event.New(event.TypeWorkerInput, "local-peer", map[string]any{"text": "hi"})
	evt.TraceID = "trace-1"
	w.forwardToRemote(context.Background(), evt)

	remote.mu.Lock()
	if len(remote.sends) != 1 || len(remote.broadcasts) != 0 {
		remote.mu.Unlock()
		t.Fatalf("want 1 directed send + 0 broadcasts, got sends=%d broadcasts=%d",
			len(remote.sends), len(remote.broadcasts))
	}
	got := remote.sends[0]
	if got.WorkerId != "remoteid" || got.Type != event.TypeWorkerInput {
		remote.mu.Unlock()
		t.Fatalf("forwarded worker_id=%q type=%q", got.WorkerId, got.Type)
	}
	if len(remote.sendTargs) == 0 || remote.sendTargs[0][0] != "b-worker" {
		remote.mu.Unlock()
		t.Fatalf("forwarded target = %v, want [b-worker]", remote.sendTargs)
	}
	if got.TraceID != "trace-1" {
		remote.mu.Unlock()
		t.Fatalf("trace not preserved: %q", got.TraceID)
	}
	remote.mu.Unlock()

	// No remote recipient set by the far side → drop, no send.
	w2, _, remote2 := testWorker("", "")
	w2.forwardToRemote(context.Background(), event.New(event.TypeWorkerInput, "p", nil))
	remote2.mu.Lock()
	defer remote2.mu.Unlock()
	if len(remote2.sends) != 0 {
		t.Fatalf("expected drop when no remote recipient, got %d sends", len(remote2.sends))
	}
}

// TestDeliverToLocalRecipient verifies a far-side event is directed at the local
// recipient and bookkeeping is dropped.
func TestDeliverToLocalRecipient(t *testing.T) {
	w, local, _ := testWorker("reason.local", "")

	// Bookkeeping from the far side is not collaboration.
	w.deliverToLocalRecipient(context.Background(), event.New(event.TypeWorkerReady, "r", nil))
	w.deliverToLocalRecipient(context.Background(), event.New(event.TypeWorkerGone, "r", nil))
	local.mu.Lock()
	if n := len(local.sends); n != 0 {
		local.mu.Unlock()
		t.Fatalf("delivered %d bookkeeping events, want 0", n)
	}
	local.mu.Unlock()

	w.deliverToLocalRecipient(context.Background(),
		event.New(event.EventType("remote.result"), "r", map[string]any{"ok": true}))

	local.mu.Lock()
	defer local.mu.Unlock()
	if len(local.sends) != 1 {
		t.Fatalf("want 1 local send, got %d", len(local.sends))
	}
	got := local.sends[0]
	if got.WorkerId != "niw-test" || got.Type != event.TypeWorkerInput {
		t.Fatalf("delivered worker_id=%q type=%q", got.WorkerId, got.Type)
	}
	if len(local.sendTargs) == 0 || local.sendTargs[0][0] != "reason.local" {
		t.Fatalf("delivered target = %v, want [reason.local]", local.sendTargs)
	}
}

// TestLocalControlGroup drives the niw.recipient.* control group over the LOCAL
// channel and verifies the local binding changes and replies are sent.
func TestLocalControlGroup(t *testing.T) {
	w, local, _ := testWorker("", "")

	// set → binds the local recipient and replies request.completed.
	callSet := event.New(TypeRecipientSet, "caller", map[string]any{"worker_id": "reason.sales"})
	callSet.RequestId = "call-1"
	w.DispatchExtension(callSet)
	l, _ := w.bindings()
	if l != "reason.sales" {
		t.Fatalf("local binding after set = %q, want reason.sales", l)
	}
	local.mu.Lock()
	if len(local.sends) == 0 || local.sends[0].Type != event.TypeRequestCompleted {
		local.mu.Unlock()
		t.Fatalf("expected a request.completed reply after set")
	}
	local.sends = nil
	local.mu.Unlock()

	// set without worker_id → request.failed, binding untouched.
	w2, local2, _ := testWorker("existing", "")
	w2.DispatchExtension(event.New(TypeRecipientSet, "caller", nil))
	if l, _ := w2.bindings(); l != "existing" {
		t.Fatalf("local binding changed on rejected set: %q", l)
	}
	local2.mu.Lock()
	if len(local2.sends) == 0 || local2.sends[0].Type != event.TypeRequestFailed {
		local2.mu.Unlock()
		t.Fatalf("expected request.failed, got %v", local2.sends)
	}
	local2.mu.Unlock()

	// unset → clears the local binding.
	w.DispatchExtension(event.New(TypeRecipientUnset, "caller", nil))
	if l, _ := w.bindings(); l != "" {
		t.Fatalf("local binding after unset = %q, want empty", l)
	}
}

// TestRemoteControlRebindsRemote verifies a far-side control event (arriving on
// the remote channel) rebinds the REMOTE recipient and replies over the remote
// channel.
func TestRemoteControlRebindsRemote(t *testing.T) {
	w, _, remote := testWorker("", "")

	callSet := event.New(TypeRecipientSet, "b-side-worker", map[string]any{"worker_id": "reason.marketing"})
	callSet.RequestId = "r-1"
	w.handleRemoteSet(callSet)

	_, r := w.bindings()
	if r != "reason.marketing" {
		t.Fatalf("remote binding after far-side set = %q, want reason.marketing", r)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.sends) == 0 || remote.sends[0].Type != event.TypeRequestCompleted {
		t.Fatalf("expected a remote reply, got %v", remote.sends)
	}
	reply := remote.sends[0]
	if reply.RequestId != "r-1" || reply.WorkerId != "remoteid" {
		t.Fatalf("remote reply id/worker = %q/%q", reply.RequestId, reply.WorkerId)
	}
}

// TestBindingsPersistRoundTrip verifies Snapshot→Restore keeps both bindings.
func TestBindingsPersistRoundTrip(t *testing.T) {
	w, _, _ := testWorker("reason.seed", "")

	snap, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Mutate after snapshot.
	w.setLocalRecipient("reason.live")
	w.setRemoteRecipient("reason.far")

	w2 := New(Config{ID: "niw-test", Bus: newMemCh("niw-test")})
	if err := w2.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	l, r := w2.bindings()
	if l != "reason.seed" || r != "" {
		t.Fatalf("restored bindings = %q/%q, want reason.seed/empty", l, r)
	}
}

// TestStartStaysUpWhenRemoteDown verifies NIW starts as a managed worker even
// when the far side is unreachable — the remote dial is a background internal
// action and must not take the worker down (which previously left a registry
// entry with no live managed worker, mis-rendered as "external" in the WebUI).
func TestStartStaysUpWhenRemoteDown(t *testing.T) {
	local := newMemCh("niw-test")
	w := New(Config{
		ID:               "niw-test",
		Bus:              local,
		RemoteURL:        "http://127.0.0.1:1", // connection refused
		RemoteWorkerID:   "remoteid",
		RemoteCredential: "",
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start with unreachable remote should succeed, got %v", err)
	}
	if l, _ := w.bindings(); l != "" {
		t.Fatalf("unexpected local binding %q", l)
	}
	// The remote link is running (and failing) in the background; the worker is
	// still locally online.
	if w.remoteNow() != nil {
		t.Fatalf("remote should be nil while unreachable, got %v", w.remoteNow())
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	cancel()
}

// TestHandleStatus verifies niw.status reports the live connection state and
// bindings, masking the credential to a boolean.
func TestHandleStatus(t *testing.T) {
	w, local, _ := testWorker("reason.local", "reason.remote")
	w.setRemote(newMemCh("connected-remote")) // simulate an active link

	w.handleStatus(event.New(TypeStatus, "caller", nil))
	local.mu.Lock()
	defer local.mu.Unlock()
	if len(local.sends) == 0 || local.sends[0].Type != event.TypeRequestCompleted {
		t.Fatalf("expected a request.completed from status, got %v", local.sends)
	}
	res, _ := local.sends[0].Payload["result"].(string)
	var st statusState
	if err := json.Unmarshal([]byte(res), &st); err != nil {
		t.Fatalf("status result not JSON: %q: %v", res, err)
	}
	if !st.Connected || st.RemoteWorkerID != "remoteid" || st.RemoteCredSet {
		t.Fatalf("unexpected status: connected=%v worker=%q credset=%v", st.Connected, st.RemoteWorkerID, st.RemoteCredSet)
	}
	if st.LocalRecipient != "reason.local" || st.RemoteRecipient != "reason.remote" {
		t.Fatalf("status bindings = %q/%q", st.LocalRecipient, st.RemoteRecipient)
	}
}

// TestHandleDialRepointsAndWakes verifies niw.dial with remote_url/worker_id
// repoints the target even while connected, and reports accordingly.
func TestHandleDialRepointsAndWakes(t *testing.T) {
	w, local, connected := testWorker("", "")
	w.setRemote(connected) // simulate an active link

	w.handleDial(event.New(TypeDial, "caller", map[string]any{
		"remote_url":       "http://peer-b:123",
		"remote_worker_id": "niw-repointed",
	}))

	// Repointing while connected tears the link; the reply says dialect.
	tgt := w.currentTarget()
	if tgt.url != "http://peer-b:123" || tgt.workerID != "niw-repointed" {
		t.Fatalf("target not repointed: %+v", tgt)
	}
	if w.remoteNow() != nil {
		t.Fatal("remote should be cleared when repointing")
	}
	local.mu.Lock()
	defer local.mu.Unlock()
	if len(local.sends) == 0 || local.sends[0].Type != event.TypeRequestCompleted {
		t.Fatalf("expected a reply, got %v", local.sends)
	}
}

// TestHandleDialAlreadyConnected verifies a no-arg dial while connected just
// reports so and leaves the link up.
func TestHandleDialAlreadyConnected(t *testing.T) {
	w, local, connected := testWorker("", "")
	w.setRemote(connected)

	w.handleDial(event.New(TypeDial, "caller", nil))
	if w.remoteNow() == nil {
		t.Fatal("no-arg dial while connected must not tear the link down")
	}
	local.mu.Lock()
	defer local.mu.Unlock()
	if len(local.sends) == 0 || local.sends[0].Type != event.TypeRequestCompleted {
		t.Fatalf("expected a reply, got %v", local.sends)
	}
}

// TestAnnounceRemoteReady verifies NIW broadcasts a worker.ready on the far
// side as its remote identity, advertising the far-side controls (recipient.*).
func TestAnnounceRemoteReady(t *testing.T) {
	w, _, remote := testWorker("", "")
	w.announceRemoteNow(remote)

	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.broadcasts) == 0 {
		t.Fatal("expected a worker.ready broadcast on the remote side")
	}
	r := remote.broadcasts[0]
	if r.Type != event.TypeWorkerReady || r.WorkerId != "remoteid" {
		t.Fatalf("remote ready = type=%q worker=%q", r.Type, r.WorkerId)
	}
	watch, _ := r.Payload["watch"].([]map[string]any)
	if len(watch) != 2 {
		t.Fatalf("ready watch has %d entries, want 2", len(watch))
	}
}

// compile-time sanity: memCh implements WorkerSideChannel.
var _ corebus.WorkerSideChannel = (*memCh)(nil)
