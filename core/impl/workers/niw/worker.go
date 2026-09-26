// Package niw provides the NIW worker — the niq interface worker that links two
// niq instances.
//
// On the LOCAL bus NIW is an ordinary host-managed worker: it embeds
// BaseWorker, registers its own identity via the bus's subscribe/publish ACL,
// and cooperates with local peers like any other worker.
//
// Its only distinction is an internal action of Start(): it dials ANOTHER niq
// instance's bus as a plain HTTP-transport remote worker (httptrans.WorkerSide),
// presenting a worker_id + credential that the REMOTE instance issued to it.
// From the remote side's point of view NIW is just an ordinary remote worker —
// the remote instance neither knows nor cares that "niw" exists.
//
// A single NIW carries a bidirectional link between the two instances, modelled
// on the lark worker's signal flow (an external endpoint bound to one internal
// worker via a DIRECTED send, never a broadcast). It keeps TWO bindings:
//
//	localRecipient  — the LOCAL worker that remote-side (B) events are
//	                  delivered to. Set by LOCAL peers.
//	remoteRecipient — the REMOTE worker that local (A) worker.input is directed
//	                  to. Set by REMOTE peers (the far side decides who on B
//	                  receives A's input).
//
// Both bindings are live, mutable, persisted properties, changed through the
// same niw.recipient.* control group (like lark's lark.reason.*), disambiguated
// by which channel the control event arrives on. A remote worker must never be
// free to directed-send into a bus it does not own: the far side grants the
// remote identity the PublishAllow that permits exactly the targeted delivery
// the far side wants (an existing bus capability enforced on every Send).
package niw

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/eventbus/transport/httptrans"
)

// Control event types NIW responds to, mirroring the lark worker's
// lark.reason.* runtime-routing group. A peer sets which worker receives the
// far side's messages. The SAME event sets a different binding depending on the
// channel it arrives on: the local channel sets the LOCAL recipient, the remote
// channel sets the REMOTE recipient.
const (
	TypeRecipientSet   event.EventType = "niw.recipient.set"
	TypeRecipientUnset event.EventType = "niw.recipient.unset"
	TypeRecipientGet   event.EventType = "niw.recipient.get"

	// A-side service controls exposed to local peers (discoverable in the
	// worker.ready watch): actively dial/redial the far side, and query the
	// live bridge status.
	TypeDial   event.EventType = "niw.dial"
	TypeStatus event.EventType = "niw.status"
)

// Config holds the configuration for a NIW.
type Config struct {
	// ID is the worker's identity on the LOCAL bus. Defaults to "niw".
	ID string
	// Bus is the local (host-managed) worker-side channel, provided by the
	// assembly layer's specConnect exactly like any other managed worker.
	Bus corebus.WorkerSideChannel

	// Recipient is the seed for the LOCAL recipient (which local worker receives
	// the far side's messages). It is the initial value only — the live binding
	// is a runtime, persisted property changed via niw.recipient.set locally.
	// Empty means "not bound until set".
	Recipient string
	// RemoteRecipient is the seed for the REMOTE recipient (which far-side worker
	// receives our local worker.input). A deployment convenience mirroring
	// Recipient: the live binding prefers a runtime niw.recipient.set from the
	// far side, but this avoids a bootstrap control event. Empty means "not bound
	// until the far side sets it".
	RemoteRecipient string
	// OnDurableChange is invoked when a binding changed and must survive a
	// restart. Baseworker machinery; nil leaves signalling off.
	OnDurableChange func()

	// RemoteURL is the base URL of the REMOTE bus (e.g. "http://host:busport").
	// NIW connects to it as an HTTP-transport remote worker.
	RemoteURL string
	// RemoteWorkerID is the identity this worker presents on the remote bus —
	// one the remote instance registered for it and issued a credential for.
	RemoteWorkerID string
	// RemoteCredential is the credential the remote instance issued for
	// RemoteWorkerID. May be empty only if the remote bus allows it.
	RemoteCredential string
}

// Worker is the niq interface worker. It holds its managed local channel via
// BaseWorker plus a private remote channel it dials at Start().
type Worker struct {
	baseworker.BaseWorker
	cfg     Config
	started bool
	cancel  context.CancelFunc
	mu      sync.Mutex

	// dialCh wakes the background link loop so a caller can force an immediate
	// (re)dial instead of waiting out the backoff.
	dialCh chan struct{}

	// remoteTarget is the far-side endpoint the link loop dials; mutable at
	// runtime via niw.dial so a caller can repoint the bridge.
	targetMu sync.RWMutex
	target   remoteTarget

	remoteMu sync.RWMutex // guards remote (nil while the far side is unreachable)
	remote   corebus.WorkerSideChannel

	muBindings  sync.RWMutex
	localRecip  string // local worker receiving far-side (B) messages
	remoteRecip string // remote worker receiving local (A) messages
}

// remoteTarget is the far-side endpoint a NIW dials.
type remoteTarget struct {
	url      string
	workerID string
	cred     string
}

// New creates a new NIW worker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "niw"
	}
	w := &Worker{
		BaseWorker:  baseworker.NewBaseWorker(id, cfg.Bus),
		cfg:         cfg,
		localRecip:  cfg.Recipient,
		remoteRecip: cfg.RemoteRecipient,
		dialCh:      make(chan struct{}, 1),
		target: remoteTarget{
			url: cfg.RemoteURL, workerID: cfg.RemoteWorkerID, cred: cfg.RemoteCredential,
		},
	}
	w.SetOnDurableChange(cfg.OnDurableChange)
	w.registerExtensions()
	return w
}

// Start starts NIW as a normal managed worker. The remote-side dial is an
// internal action: it is attempted in the background (with retry), so NIW stays
// online and cooperates locally even when the far side is unreachable — exactly
// as if the dial were "read a file / call an API". Start never fails for a
// remote that is down.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("niw: already started")
	}

	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.started = true

	// Local event loop + durable signalling, as a managed worker.
	localCh, _ := w.Channel.Receive(runCtx)
	w.StartDurableLoop(runCtx)
	go w.watchLocal(runCtx, localCh)
	// The remote-side bridge runs in the background and reconnects on its own.
	go w.runRemoteLink(runCtx)

	w.AnnounceReady("niw", nil) // the niw.recipient.* tools ride in the watch field via registerExtensions
	log.Printf("[niw %s] started (local); remote link to %s running in background", w.ID(), w.cfg.RemoteURL)
	return nil
}

// runRemoteLink keeps the far-side link alive: connect → bridge → on drop,
// clear the channel and retry with backoff. It is NIW's private internal
// action; the niq runtime does not care and the worker stays local-online.
func (w *Worker) runRemoteLink(runCtx context.Context) {
	const (
		initial = time.Second
		max     = 30 * time.Second
	)
	delay := initial
	for {
		if runCtx.Err() != nil {
			return
		}
		tgt := w.currentTarget()
		remote := httptrans.NewWorkerSide(tgt.url, tgt.workerID, tgt.cred)
		connectCtx, cancel := context.WithCancel(runCtx)
		if err := remote.Connect(connectCtx, "http://remote"); err != nil {
			cancel()
			log.Printf("[niw %s] remote bus %s unreachable (%v); retry in %s", w.ID(), tgt.url, err, delay)
			if !w.sleepOrDial(runCtx, delay) {
				return
			}
			delay = nextBackoff(delay, max)
			continue
		}

		remoteCh, err := remote.Receive(connectCtx)
		if err != nil {
			cancel()
			_ = remote.Close()
			log.Printf("[niw %s] remote receive failed: %v", w.ID(), err)
			if !w.sleepOrDial(runCtx, delay) {
				return
			}
			delay = nextBackoff(delay, max)
			continue
		}

		w.setRemote(remote)
		delay = initial
		log.Printf("[niw %s] bridged to remote bus %s as %s", w.ID(), tgt.url, tgt.workerID)
		// Behave like an ordinary remote worker on the far side: announce presence
		// (worker.ready) so the far side's reason/directory workers can discover
		// and call us. Best-effort — the far side's PublishAllow gates it.
		w.announceRemoteNow(remote)
		w.watchRemote(connectCtx, remoteCh)

		// The remote stream ended (disconnect) or the worker stopped. Release the
		// channel and retry unless we're shutting down.
		w.setRemote(nil)
		_ = remote.Close()
		cancel()
		if runCtx.Err() != nil {
			return
		}
		if !w.sleepOrDial(runCtx, delay) {
			return
		}
	}
}

// currentTarget returns the far-side endpoint the link loop dials.
func (w *Worker) currentTarget() remoteTarget {
	w.targetMu.RLock()
	defer w.targetMu.RUnlock()
	return w.target
}

// setTarget updates the far-side endpoint the link loop dials.
func (w *Worker) setTarget(t remoteTarget) {
	w.targetMu.Lock()
	w.target = t
	w.targetMu.Unlock()
}

// wakeDial nudges the link loop to dial immediately, coalescing bursts.
func (w *Worker) wakeDial() {
	select {
	case w.dialCh <- struct{}{}:
	default:
	}
}

// sleepOrDial sleeps d unless the ctx ends or a manual dial is requested;
// reports whether the loop should proceed to (re)dial.
func (w *Worker) sleepOrDial(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-w.dialCh:
		return true
	case <-ctx.Done():
		return false
	}
}

// nextBackoff doubles d toward max.
func nextBackoff(d, max time.Duration) time.Duration {
	d *= 2
	if d > max {
		return max
	}
	return d
}

// setRemote installs or clears the active remote channel under lock.
func (w *Worker) setRemote(r corebus.WorkerSideChannel) {
	w.remoteMu.Lock()
	w.remote = r
	w.remoteMu.Unlock()
}

// remoteNow returns the active remote channel, or nil when unreachable.
func (w *Worker) remoteNow() corebus.WorkerSideChannel {
	w.remoteMu.RLock()
	defer w.remoteMu.RUnlock()
	return w.remote
}

// Stop stops the NIW worker: cancels the remote link and closes the channel.
func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	w.started = false
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	if r := w.remoteNow(); r != nil {
		_ = r.Close()
	}
	w.setRemote(nil)
	log.Printf("[niw %s] stopped", w.ID())
	return nil
}

// ── Persistence ──

// bindingState is NIW's durable state: both recipient bindings.
type bindingState struct {
	Local  string `json:"local,omitempty"`
	Remote string `json:"remote,omitempty"`
}

// Snapshot captures both bindings so they survive a restart.
func (w *Worker) Snapshot() ([]byte, error) {
	l, r := w.bindings()
	return json.Marshal(bindingState{Local: l, Remote: r})
}

// Restore rehydrates both bindings. Called after construction and before Start.
func (w *Worker) Restore(state []byte) error {
	var s bindingState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("niw restore: %w", err)
	}
	w.muBindings.Lock()
	w.localRecip = s.Local
	w.remoteRecip = s.Remote
	w.muBindings.Unlock()
	return nil
}

// ── Bindings ──

func (w *Worker) bindings() (local, remote string) {
	w.muBindings.RLock()
	defer w.muBindings.RUnlock()
	return w.localRecip, w.remoteRecip
}

// setLocalRecipient rebinds the far-side→local delivery target and marks the
// change durable.
func (w *Worker) setLocalRecipient(id string) {
	w.muBindings.Lock()
	w.localRecip = id
	w.muBindings.Unlock()
	w.NotifyDurableChange()
}

// setRemoteRecipient rebinds the local→far-side delivery target and marks the
// change durable.
func (w *Worker) setRemoteRecipient(id string) {
	w.muBindings.Lock()
	w.remoteRecip = id
	w.muBindings.Unlock()
	w.NotifyDurableChange()
}

// ── Local bridge loop ──

// watchLocal drains the local channel: local worker.input is directed to the
// REMOTE recipient (set by the far side), the niw.recipient.* control group is
// dispatched as extensions. Exits when the local channel closes (suspend/stop).
func (w *Worker) watchLocal(ctx context.Context, localCh <-chan event.Event) {
	for {
		select {
		case evt, ok := <-localCh:
			if !ok {
				return
			}
			switch evt.Type {
			case event.TypeWorkerInput:
				w.forwardToRemote(ctx, evt)
			case event.TypeWorkerDiscover:
				// A local directory asks who is here: answer directly with this
				// worker's contract, so the roster/list_workers sees us.
				if evt.WorkerId != "" && evt.WorkerId != w.ID() {
					w.AnnounceReadyTo(evt.WorkerId, "niw", nil, true)
				}
			default:
				if !w.DispatchExtension(evt) {
					log.Printf("[niw %s] no handler for local event %s", w.ID(), evt.Type)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// ── Remote bridge loop ──

// watchRemote drains the remote channel. A control event (niw.recipient.set /
// unset) from the far side rebinds the REMOTE recipient — the far side deciding
// who on its own bus receives our input. Any other event is directed to the
// LOCAL recipient. Exits when the remote stream ends (disconnect).
func (w *Worker) watchRemote(ctx context.Context, remoteCh <-chan event.Event) {
	for {
		select {
		case evt, ok := <-remoteCh:
			if !ok {
				return
			}
			switch evt.Type {
			case TypeRecipientSet:
				w.handleRemoteSet(evt)
			case TypeRecipientUnset:
				w.handleRemoteUnset(evt)
			case event.TypeWorkerDiscover:
				// The far side's directory asks who is there; answer directly so its
				// roster/list_workers sees us as a peer. Only if a far-side caller
				// other than ourselves asked.
				if evt.WorkerId != "" {
					w.announceRemoteTo(evt.WorkerId)
				}
			default:
				w.deliverToLocalRecipient(ctx, evt)
			}
		case <-ctx.Done():
			return
		}
	}
}

// forwardToRemote directs a local worker.input to the far side's recipient — a
// REMOTE worker the far side named, not a broadcast. The far side's bus enforces
// that the remote identity's PublishAllow permits sending to that target; if the
// far side granted nothing (or the target differs), the send is rejected there.
func (w *Worker) forwardToRemote(ctx context.Context, evt event.Event) {
	remote := w.remoteNow()
	if remote == nil {
		return
	}
	_, remoteRecip := w.bindings()
	if remoteRecip == "" {
		log.Printf("[niw %s] local input: no remote recipient set by the far side, dropping", w.ID())
		return
	}
	out := event.New(event.TypeWorkerInput, w.cfg.RemoteWorkerID, evt.Payload)
	out.RequestId = evt.RequestId
	out.TraceID = evt.TraceID
	if err := remote.Send(ctx, out, remoteRecip); err != nil {
		log.Printf("[niw %s] send to remote recipient %s: %v", w.ID(), remoteRecip, err)
	}
}

// deliverToLocalRecipient hands a far-side event to the LOCAL recipient as a
// directed send — the "configured receiver" that distinguishes NIW from a
// broadcast relay. Far-side bus bookkeeping (worker.ready / gone) of its own
// workers is not collaboration and is dropped.
func (w *Worker) deliverToLocalRecipient(ctx context.Context, evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerReady, event.TypeWorkerGone, event.TypeWorkerDiscover:
		return
	}
	localRecip, _ := w.bindings()
	if localRecip == "" {
		log.Printf("[niw %s] remote event %s: no local recipient, dropping", w.ID(), evt.Type)
		return
	}
	out := event.New(event.TypeWorkerInput, w.ID(), evt.Payload)
	out.RequestId = evt.RequestId
	out.TraceID = evt.TraceID
	out.Transient = evt.Transient
	if err := w.Channel.Send(ctx, out, localRecip); err != nil {
		log.Printf("[niw %s] deliver to recipient %s: %v", w.ID(), localRecip, err)
	}
}

// ── Control extensions (niw.recipient.*) — local channel ──

// registerExtensions declares the peer-callable routing controls, mirroring the
// lark worker's lark.reason.* group. Non-SelfOnly so a control plane / reason
// peer can discover and call them.
func (w *Worker) registerExtensions() {
	w.Register(baseworker.Extension{
		Event:       TypeRecipientSet,
		Description: "Bind which local worker receives the far side's messages.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"worker_id": map[string]any{"type": "string", "description": "Local worker id to bind as the recipient."},
			},
			"required": []any{"worker_id"},
		},
	}, func(evt event.Event) { w.handleLocalSet(evt) })

	w.Register(baseworker.Extension{
		Event:       TypeRecipientUnset,
		Description: "Clear the local recipient: stop delivering far-side messages locally.",
	}, func(evt event.Event) { w.handleLocalUnset(evt) })

	w.Register(baseworker.Extension{
		Event:       TypeRecipientGet,
		Description: "Return the current local/remote recipient bindings as JSON.",
	}, func(evt event.Event) { w.handleGet(evt) })

	// A-side service controls: actively (re)dial the far side and query the
	// live bridge status. Non-SelfOnly so a local reason/control peer can
	// discover and drive the link.
	w.Register(baseworker.Extension{
		Event:       TypeDial,
		Description: "Actively connect/reconnect the bridge to the far side. Optionally repoint it with remote_url / remote_worker_id / remote_credential.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"remote_url":        map[string]any{"type": "string", "description": "(Optional) Repoint the far-side bus URL."},
				"remote_worker_id":  map[string]any{"type": "string", "description": "(Optional) Repoint the identity presented on the far side."},
				"remote_credential": map[string]any{"type": "string", "description": "(Optional) Repoint the far-side credential."},
			},
		},
	}, func(evt event.Event) { w.handleDial(evt) })

	w.Register(baseworker.Extension{
		Event:       TypeStatus,
		Description: "Return the current bridge status: connected, remote target, and recipient bindings.",
	}, func(evt event.Event) { w.handleStatus(evt) })
}

func (w *Worker) handleLocalSet(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	wid := baseworker.ArgString(tc.Args, "worker_id")
	if wid == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, "worker_id is required", tc.TraceID)
		return
	}
	w.setLocalRecipient(wid)
	w.ReplyCompleted(tc.CallerID, tc.CallID, "local recipient set to "+wid, tc.TraceID)
	log.Printf("[niw %s] local recipient set to %s by %s", w.ID(), wid, tc.CallerID)
}

func (w *Worker) handleLocalUnset(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	w.setLocalRecipient("")
	w.ReplyCompleted(tc.CallerID, tc.CallID, "local recipient cleared", tc.TraceID)
}

func (w *Worker) handleGet(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	l, r := w.bindings()
	data, _ := json.Marshal(bindingState{Local: l, Remote: r})
	w.ReplyCompleted(tc.CallerID, tc.CallID, string(data), tc.TraceID)
}

// statusState is the live bridge status served by niw.status.
type statusState struct {
	Connected       bool   `json:"connected"`
	RemoteURL       string `json:"remote_url,omitempty"`
	RemoteWorkerID  string `json:"remote_worker_id,omitempty"`
	RemoteCredSet   bool   `json:"remote_credential_set"`
	LocalRecipient  string `json:"local_recipient,omitempty"`
	RemoteRecipient string `json:"remote_recipient,omitempty"`
}

// handleDial actively (re)establishes the remote link. With no args it forces
// an immediate redial of the current target (waking the backoff loop; if already
// connected it reports so and leaves the link alone). With target args it
// repoints the far-side endpoint and reconnects to it.
func (w *Worker) handleDial(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)

	url, _ := tc.Args["remote_url"].(string)
	workerID, _ := tc.Args["remote_worker_id"].(string)
	cred, _ := tc.Args["remote_credential"].(string)
	repoint := url != "" || workerID != ""

	tgt := w.currentTarget()
	if repoint {
		// Merge over the existing target: optional fields only replace when given.
		if url != "" {
			tgt.url = url
		}
		if workerID != "" {
			tgt.workerID = workerID
		}
		if cred != "" {
			tgt.cred = cred
		}
		w.setTarget(tgt)
	}

	connected := w.remoteNow() != nil
	w.wakeDial()
	// If repointing while connected, tear the current link down so the loop
	// re-establishes against the new target instead of leaving the old one up.
	if repoint && connected {
		if r := w.remoteNow(); r != nil {
			_ = r.Close()
		}
		w.setRemote(nil)
		connected = false
	}

	if connected {
		w.ReplyCompleted(tc.CallerID, tc.CallID, "already connected to "+tgt.url, tc.TraceID)
		return
	}
	w.ReplyCompleted(tc.CallerID, tc.CallID, "dialing "+tgt.url, tc.TraceID)
	log.Printf("[niw %s] manual dial requested by %s -> %s", w.ID(), tc.CallerID, tgt.url)
}

// handleStatus reports the live bridge state.
func (w *Worker) handleStatus(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	tgt := w.currentTarget()
	l, r := w.bindings()
	st := statusState{
		Connected:       w.remoteNow() != nil,
		RemoteURL:       tgt.url,
		RemoteWorkerID:  tgt.workerID,
		RemoteCredSet:   tgt.cred != "",
		LocalRecipient:  l,
		RemoteRecipient: r,
	}
	data, _ := json.Marshal(st)
	w.ReplyCompleted(tc.CallerID, tc.CallID, string(data), tc.TraceID)
}

// ── Control events — remote channel ──

// handleRemoteSet applies a far-side control event rebinding the REMOTE
// recipient, and replies over the remote channel to the far-side caller.
func (w *Worker) handleRemoteSet(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	wid := baseworker.ArgString(tc.Args, "worker_id")
	if wid == "" {
		w.replyRemote(tc, false, "worker_id is required")
		return
	}
	w.setRemoteRecipient(wid)
	w.replyRemote(tc, true, "remote recipient set to "+wid)
	log.Printf("[niw %s] remote recipient set to %s by %s", w.ID(), wid, tc.CallerID)
}

func (w *Worker) handleRemoteUnset(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	w.setRemoteRecipient("")
	w.replyRemote(tc, true, "remote recipient cleared")
}

// replyRemote answers a remote control event back over the remote channel,
// echoing the far-side caller's request id and trace.
func (w *Worker) replyRemote(tc baseworker.ToolCall, ok bool, msg string) {
	remote := w.remoteNow()
	if remote == nil {
		return
	}
	typ := event.TypeRequestCompleted
	key := "result"
	if !ok {
		typ = event.TypeRequestFailed
		key = "error"
	}
	out := event.New(typ, w.cfg.RemoteWorkerID, map[string]any{key: msg})
	out.RequestId = tc.CallID
	out.TraceID = tc.TraceID
	if err := remote.Send(context.Background(), out, tc.CallerID); err != nil {
		log.Printf("[niw %s] reply to remote %s: %v", w.ID(), tc.CallerID, err)
	}
}

// remoteReadyPayload is the worker.ready this remote identity announces on the
// far side. It advertises the controls the FAR side can use on us: re-binding
// who on B receives A's input. The far side's ready handling will only surface
// these as callable if it keeps non-reason peers (niw is type "niw").
func (w *Worker) remoteReadyPayload() map[string]any {
	return map[string]any{
		"type": "niw",
		"watch": []map[string]any{
			{"event": "niw.recipient.set", "desc": "Bind which local worker receives A's input.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"worker_id": map[string]any{"type": "string", "description": "Local worker id to bind as the remote recipient."},
					},
					"required": []any{"worker_id"},
				},
			},
			{"event": "niw.recipient.unset", "desc": "Clear who receives A's input."},
		},
	}
}

// announceRemoteNow broadcasts this worker's presence on the far side as its
// remote identity. Best-effort: the far side's PublishAllow for this identity
// gates whether the broadcast is permitted — an unenabled announcement is simply
// dropped there (and logged as a denied publish), which matches the "far side
// controls what a remote worker may publish" security model.
func (w *Worker) announceRemoteNow(remote corebus.WorkerSideChannel) {
	out := event.New(event.TypeWorkerReady, w.cfg.RemoteWorkerID, w.remoteReadyPayload())
	out.ExcludeWorkerID = w.cfg.RemoteWorkerID
	out.Transient = true
	if err := remote.Broadcast(context.Background(), out); err != nil {
		log.Printf("[niw %s] announce ready on remote %s: %v", w.ID(), w.cfg.RemoteURL, err)
	}
	log.Printf("[niw %s] announced ready on remote bus as %s", w.ID(), w.cfg.RemoteWorkerID)
}

// announceRemoteTo replies to a far-side worker.discover by sending this
// worker's ready directly to the asker.
func (w *Worker) announceRemoteTo(target string) {
	remote := w.remoteNow()
	if remote == nil || target == "" {
		return
	}
	out := event.New(event.TypeWorkerReady, w.cfg.RemoteWorkerID, w.remoteReadyPayload())
	out.Transient = true
	if err := remote.Send(context.Background(), out, target); err != nil {
		log.Printf("[niw %s] discover reply to %s: %v", w.ID(), target, err)
	}
}
