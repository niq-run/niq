package niq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/iface"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// remote is a WorkerSideChannel double standing in for the far-side bus.
type remote struct {
	mu     sync.Mutex
	sent   []event.Event
	target [][]string
	in     chan event.Event
}

func newRemote() *remote { return &remote{in: make(chan event.Event, 16)} }

func (r *remote) ID() string { return "far" }
func (r *remote) Send(_ context.Context, evt event.Event, targets ...string) error {
	r.mu.Lock()
	r.sent = append(r.sent, evt)
	r.target = append(r.target, append([]string(nil), targets...))
	r.mu.Unlock()
	return nil
}
func (r *remote) Broadcast(_ context.Context, evt event.Event) error {
	r.mu.Lock()
	r.sent = append(r.sent, evt)
	r.mu.Unlock()
	return nil
}
func (r *remote) Receive(context.Context) (<-chan event.Event, error) { return r.in, nil }
func (r *remote) Close() error                                        { return nil }

func (r *remote) last() (event.Event, []string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sent) == 0 {
		return event.Event{}, nil, false
	}
	return r.sent[len(r.sent)-1], r.target[len(r.target)-1], true
}

// local is the local bus handle the interface worker hands the backend.
type local struct {
	mu     sync.Mutex
	sent   []event.Event
	target [][]string
	types  []event.EventType
	fn     func(event.Event)
}

func (l *local) ID() string { return "niq-a" }
func (l *local) Send(_ context.Context, evt event.Event, targets ...string) error {
	l.mu.Lock()
	l.sent = append(l.sent, evt)
	l.target = append(l.target, append([]string(nil), targets...))
	l.mu.Unlock()
	return nil
}
func (l *local) Broadcast(_ context.Context, evt event.Event) error {
	l.mu.Lock()
	l.sent = append(l.sent, evt)
	l.mu.Unlock()
	return nil
}
func (l *local) Request(context.Context, string, event.Event) (event.Event, error) {
	return event.Event{}, nil
}

// OnEvent records the registration (the direction tests drive it manually) so a
// test can assert WHAT the backend asked to hear.
func (l *local) OnEvent(fn func(event.Event), types ...event.EventType) iface.Subscription {
	l.mu.Lock()
	l.fn, l.types = fn, types
	l.mu.Unlock()
	return nopSub{}
}

func (l *local) last() (event.Event, []string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.sent) == 0 {
		return event.Event{}, nil, false
	}
	return l.sent[len(l.sent)-1], l.target[len(l.target)-1], true
}

type nopSub struct{}

func (nopSub) Close() {}

// host captures tool replies.
type host struct {
	mu       sync.Mutex
	reps     []map[string]any
	fail     []string
	patches  []map[string]any
	patchErr error
}

func (h *host) Register(baseworker.Extension, baseworker.ExtensionHandler) {}
func (h *host) RegisterReadme(string, func() string, map[string]any)       {}
func (h *host) ReplyCompleted(_, callID string, result any, _ string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reps = append(h.reps, map[string]any{"call": callID, "result": result})
}
func (h *host) ReplyFailed(_, callID, msg, _ string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fail = append(h.fail, msg)
}

// PatchConfig records the merge patch the backend asked to persist. patchErr
// simulates a host whose declaration write fails.
func (h *host) PatchConfig(patch map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.patchErr != nil {
		return h.patchErr
	}
	if patch != nil {
		h.patches = append(h.patches, patch)
	}
	return nil
}

// newBackend builds a backend with the given seeds and a wired local handle.
func newBackend(t *testing.T, cfg Config) (*Backend, *local) {
	t.Helper()
	if cfg.ID == "" {
		cfg.ID = "niq-a"
	}
	b := New(cfg)
	l := &local{}
	b.SetHandler(l)
	return b, l
}

// TestForwardsLocalInputToFarSideRecipient verifies the local→far direction is a
// DIRECTED send to the recipient the far side chose, under the remote identity,
// with the request/trace ids preserved.
func TestForwardsLocalInputToFarSideRecipient(t *testing.T) {
	b, l := newBackend(t, Config{ID: "niq-a", RemoteWorkerID: "far-id", RemoteRecipient: "reason.b"})
	r := newRemote()
	b.setRemote(r)

	l.mu.Lock()
	fn := l.fn
	l.mu.Unlock()
	if fn == nil {
		t.Fatal("SetHandler did not register inbound interest")
	}

	in := event.New(event.TypeWorkerInput, "reason.a", map[string]any{"content": "hi"})
	in.RequestId = "r1"
	in.TraceID = "t1"
	fn(in)

	sent, targets, ok := r.last()
	if !ok {
		t.Fatal("nothing was forwarded to the far side")
	}
	if sent.Type != event.TypeWorkerInput || sent.WorkerId != "far-id" {
		t.Fatalf("forwarded = %+v, want worker.input as far-id", sent)
	}
	if sent.RequestId != "r1" || sent.TraceID != "t1" {
		t.Fatalf("ids not preserved: %+v", sent)
	}
	if len(targets) != 1 || targets[0] != "reason.b" {
		t.Fatalf("targets = %v, want the far-side recipient only", targets)
	}
}

// TestLocalInputDroppedWithoutRecipientOrLink verifies the two silent-drop cases
// are silent: an unbound far side, and no link at all.
func TestLocalInputDroppedWithoutRecipientOrLink(t *testing.T) {
	b, l := newBackend(t, Config{ID: "niq-a", RemoteWorkerID: "far-id"})
	l.mu.Lock()
	fn := l.fn
	l.mu.Unlock()

	// No link yet.
	fn(event.New(event.TypeWorkerInput, "reason.a", nil))
	if _, _, ok := l.last(); ok {
		t.Fatal("forwarded without a far-side link")
	}

	// Link but no remote recipient.
	r := newRemote()
	b.setRemote(r)
	fn(event.New(event.TypeWorkerInput, "reason.a", nil))
	if _, _, ok := r.last(); ok {
		t.Fatal("forwarded without a far-side recipient binding")
	}
}

// TestFarSideInputGoesToLocalRecipient verifies the far→local direction: only
// collaborative worker.input crosses (never the far side's lifecycle traffic), and
// it is directed to the local recipient as this worker.
func TestFarSideInputGoesToLocalRecipient(t *testing.T) {
	b, l := newBackend(t, Config{ID: "niq-a", Recipient: "reason.a"})

	// Lifecycle noise must not be relayed as conversation.
	b.deliverToLocalRecipient(event.New(event.TypeWorkerReady, "far-id", nil))
	if _, _, ok := l.last(); ok {
		t.Fatal("far-side worker.ready was relayed locally")
	}

	in := event.New(event.TypeWorkerInput, "far-id", map[string]any{"content": "hello"})
	in.RequestId, in.TraceID = "r2", "t2"
	b.deliverToLocalRecipient(in)

	sent, targets, ok := l.last()
	if !ok {
		t.Fatal("far-side input was not delivered locally")
	}
	if sent.Type != event.TypeWorkerInput || sent.WorkerId != "niq-a" {
		t.Fatalf("delivered = %+v, want worker.input from this worker", sent)
	}
	if sent.RequestId != "r2" || sent.TraceID != "t2" {
		t.Fatalf("ids not preserved: %+v", sent)
	}
	if len(targets) != 1 || targets[0] != "reason.a" {
		t.Fatalf("targets = %v, want the local recipient only", targets)
	}
}

// TestFarSideControlEventRebindsRemoteRecipient verifies the far side can re-point
// its recipient, that the change is marked durable, and that the far side gets a
// reply on its own channel.
func TestFarSideControlEventRebindsRemoteRecipient(t *testing.T) {
	var durable int
	b, _ := newBackend(t, Config{ID: "niq-a", RemoteWorkerID: "far-id", OnDurableChange: func() { durable++ }})
	r := newRemote()
	b.setRemote(r)

	evt := event.New(TypeRecipientSet, "reason.b", map[string]any{"worker_id": "reason.c"})
	evt.RequestId, evt.TraceID = "ctl-1", "ctl-trace"
	b.handleRemoteSet(evt)

	if _, remoteRecip := b.bindings(); remoteRecip != "reason.c" {
		t.Fatalf("remote recipient = %q, want reason.c", remoteRecip)
	}
	if durable != 1 {
		t.Fatalf("durable notifications = %d, want 1", durable)
	}
	reply, targets, ok := r.last()
	if !ok || reply.Type != event.TypeRequestCompleted {
		t.Fatalf("no reply to the far-side control event: %+v", reply)
	}
	if reply.RequestId != "ctl-1" || reply.TraceID != "ctl-trace" {
		t.Fatalf("reply ids = %+v", reply)
	}
	if len(targets) != 1 || targets[0] != "reason.b" {
		t.Fatalf("reply targets = %v, want the far-side caller", targets)
	}

	// A malformed control event is answered with a failure, not a silent drop.
	b.handleRemoteSet(event.New(TypeRecipientSet, "reason.b", map[string]any{}))
	reply, _, _ = r.last()
	if reply.Type != event.TypeRequestFailed {
		t.Fatalf("missing worker_id reply = %s, want request.failed", reply.Type)
	}
}

// TestBindingsSurviveARestart verifies the backend's durable state: the two
// recipient bindings, and nothing else.
func TestBindingsSurviveARestart(t *testing.T) {
	b, _ := newBackend(t, Config{ID: "niq-a"})
	b.setLocalRecipient("reason.a")
	b.setRemoteRecipient("reason.b")

	snap, err := b.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var state bindingsState
	if err := json.Unmarshal(snap, &state); err != nil {
		t.Fatalf("snapshot shape: %v", err)
	}
	if state.Local != "reason.a" || state.Remote != "reason.b" {
		t.Fatalf("snapshot = %+v", state)
	}

	restored, _ := newBackend(t, Config{ID: "niq-a"})
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if l, r := restored.bindings(); l != "reason.a" || r != "reason.b" {
		t.Fatalf("bindings = %q/%q", l, r)
	}
	if err := restored.Restore([]byte("not json")); err == nil {
		t.Fatal("a malformed snapshot was accepted")
	}
}

// TestStatusAndRecipientTools verifies the local tool surface answers with the
// live state (and never renders the far-side credential).
func TestStatusAndRecipientTools(t *testing.T) {
	b, _ := newBackend(t, Config{
		ID: "niq-a", RemoteURL: "http://far", RemoteWorkerID: "far-id",
		RemoteCredential: "secret", Recipient: "reason.a",
	})
	h := &host{}

	// The tool set is registered on the host, so drive the handlers directly: the
	// registration lives in RegisterTools, which the builder calls.
	b.RegisterTools(h)
	if len(h.reps)+len(h.fail) != 0 {
		t.Fatal("RegisterTools answered something")
	}

	// status reads the live bindings + target.
	evt := event.New(TypeStatus, "reason.x", map[string]any{})
	evt.RequestId = "call-status"
	b.handleStatus(evt, h)
	if len(h.reps) != 1 {
		t.Fatalf("status replies = %+v", h.reps)
	}
	st, ok := h.reps[0]["result"].(statusState)
	if !ok {
		t.Fatalf("status result = %T", h.reps[0]["result"])
	}
	if st.RemoteURL != "http://far" || st.RemoteWorkerID != "far-id" || !st.RemoteCredSet || st.LocalRecipient != "reason.a" {
		t.Fatalf("status = %+v", st)
	}
	if strings.Contains(b.readmeText(), "secret") {
		t.Fatal("worker.readme rendered the far-side credential")
	}
}

// TestDialRepointsAndReports verifies niq.dial: it merges the given target fields,
// reports "dialing", and waking the loop is observable through the target.
func TestDialRepointsAndReports(t *testing.T) {
	b, _ := newBackend(t, Config{ID: "niq-a", RemoteURL: "http://a", RemoteWorkerID: "id-a"})
	h := &host{}

	evt := event.New(TypeDial, "reason.x", map[string]any{"remote_url": "http://b"})
	evt.RequestId = "dial-1"
	b.handleDial(baseworker.ParseToolCall(evt), h)

	tgt := b.currentTarget()
	if tgt.url != "http://b" || tgt.workerID != "id-a" {
		t.Fatalf("target = %+v, want url repointed and id kept", tgt)
	}
	if len(h.reps) != 1 {
		t.Fatalf("dial replies = %+v", h.reps)
	}
	if msg, _ := h.reps[0]["result"].(string); !strings.Contains(msg, "dialing") {
		t.Fatalf("dial reply = %v", h.reps[0]["result"])
	}
}

// TestDialPersistsTarget verifies that a repoint is persisted by default: the
// changed keys reach the worker's declaration writer, and the reply says saved.
func TestDialPersistsTarget(t *testing.T) {
	b, _ := newBackend(t, Config{
		ID: "niq-a", RemoteURL: "http://a", RemoteWorkerID: "id-a", RemoteCredential: "old",
	})
	h := &host{}

	evt := event.New(TypeDial, "reason.x", map[string]any{"remote_url": "http://b", "remote_credential": "new"})
	evt.RequestId = "dial-1"
	b.handleDial(baseworker.ParseToolCall(evt), h)

	if len(h.patches) != 1 {
		t.Fatalf("patches = %+v, want one", h.patches)
	}
	// Only the changed keys are sent: a merge patch, so other params survive.
	want := map[string]any{"remote_url": "http://b", "remote_credential": "new"}
	if len(h.patches[0]) != len(want) || h.patches[0]["remote_url"] != want["remote_url"] || h.patches[0]["remote_credential"] != want["remote_credential"] {
		t.Fatalf("patch = %+v, want %+v", h.patches[0], want)
	}
	if tgt := b.currentTarget(); tgt.url != "http://b" || tgt.cred != "new" {
		t.Fatalf("target = %+v, want the repointed values", tgt)
	}
	if msg, _ := h.reps[0]["result"].(string); !strings.Contains(msg, "saved") {
		t.Fatalf("dial reply = %v, want it to mention saving", h.reps[0]["result"])
	}
}

// TestDialPersistsCredentialReference keeps an `env:` reference a reference: a
// repoint that only moves the URL must not turn the resolved token into
// plaintext in the worker config.
func TestDialPersistsCredentialReference(t *testing.T) {
	b, _ := newBackend(t, Config{
		ID: "niq-a", RemoteURL: "http://a", RemoteWorkerID: "id-a",
		RemoteCredential:  "env:FAR_TOKEN",
		ResolveCredential: func(raw string) string { return "resolved-" + raw },
	})
	h := &host{}

	evt := event.New(TypeDial, "reason.x", map[string]any{"remote_url": "http://b"})
	evt.RequestId = "dial-1"
	b.handleDial(baseworker.ParseToolCall(evt), h)

	if len(h.patches) != 1 || len(h.patches[0]) != 1 {
		t.Fatalf("patch = %+v, want only remote_url", h.patches)
	}
	if _, ok := h.patches[0]["remote_credential"]; ok {
		t.Fatalf("patch = %+v, must not restate the credential", h.patches[0])
	}
	if tgt := b.currentTarget(); tgt.credRaw != "env:FAR_TOKEN" || tgt.cred != "resolved-env:FAR_TOKEN" {
		t.Fatalf("target = %+v, want the reference kept and resolved for dialing", tgt)
	}
}

// TestDialSaveFailureLeavesTargetAlone: when the repoint cannot be persisted the
// call fails and the old target stays in force, so memory and config never
// disagree.
func TestDialSaveFailureLeavesTargetAlone(t *testing.T) {
	b, _ := newBackend(t, Config{ID: "niq-a", RemoteURL: "http://a", RemoteWorkerID: "id-a"})
	h := &host{patchErr: errors.New("manifest is read-only")}

	evt := event.New(TypeDial, "reason.x", map[string]any{"remote_url": "http://b"})
	evt.RequestId = "dial-1"
	b.handleDial(baseworker.ParseToolCall(evt), h)

	if len(h.fail) != 1 || !strings.Contains(h.fail[0], "read-only") {
		t.Fatalf("failures = %+v, want one reporting the save error", h.fail)
	}
	if tgt := b.currentTarget(); tgt.url != "http://a" {
		t.Fatalf("target = %+v, want the old target kept", tgt)
	}
}

// TestDialWithoutConfigStoreDegrades: an embedded host has no declaration
// writer. The repoint still applies for this process, and the reply says it was
// not saved instead of pretending it was.
func TestDialWithoutConfigStoreDegrades(t *testing.T) {
	b, _ := newBackend(t, Config{ID: "niq-a", RemoteURL: "http://a", RemoteWorkerID: "id-a"})
	h := &unwiredHost{}

	evt := event.New(TypeDial, "reason.x", map[string]any{"remote_url": "http://b"})
	evt.RequestId = "dial-1"
	b.handleDial(baseworker.ParseToolCall(evt), h)

	if tgt := b.currentTarget(); tgt.url != "http://b" {
		t.Fatalf("target = %+v, want the in-memory repoint to apply", tgt)
	}
	if len(h.reps) != 1 {
		t.Fatalf("replies = %+v", h.reps)
	}
	if msg, _ := h.reps[0]["result"].(string); !strings.Contains(msg, "not saved") {
		t.Fatalf("dial reply = %v, want it to report that nothing was saved", h.reps[0]["result"])
	}
}

// unwiredHost is a host with no declaration writer: baseworker reports
// ErrConfigUnavailable, which the backend treats as "memory only".
type unwiredHost struct{ host }

func (h *unwiredHost) PatchConfig(map[string]any) error {
	return fmt.Errorf("%w", baseworker.ErrConfigUnavailable)
}

// TestLinkRetriesAndStopsCleanly verifies Start/Stop on the backend without a
// reachable far side: the link loop keeps retrying (no error out of Start) and
// Stop returns promptly.
func TestLinkRetriesAndStopsCleanly(t *testing.T) {
	b, _ := newBackend(t, Config{
		ID: "niq-a", RemoteURL: "http://127.0.0.1:1", RemoteWorkerID: "far-id", RemoteCredential: "x",
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Give the loop a moment to attempt and back off; it must not have blocked.
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() { _ = b.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop hung")
	}
	cancel()
}

var _ corebus.WorkerSideChannel = (*remote)(nil)
