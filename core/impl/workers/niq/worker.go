// Package niq provides the niq worker: a worker whose backend is another niq
// instance.
//
// It is the concrete case of an interface worker (core/impl/iface). One side
// faces this bus — it is an ordinary managed worker there, with an identity,
// ACL, presence and tools. The other side is a BACKEND that dials another niq
// instance's bus as a plain HTTP-transport remote worker, presenting the
// identity + credential that instance issued for it. The far side sees an
// ordinary remote worker; it neither knows nor cares that this package exists.
//
// One niq worker carries a BIDIRECTIONAL link, modelled on the lark worker's
// signal flow (an external endpoint bound to one internal worker via a DIRECTED
// send, never a broadcast). It keeps two bindings:
//
//	localRecipient  — the LOCAL worker far-side events are delivered to, set by
//	                  LOCAL peers (niq.recipient.set on this bus);
//	remoteRecipient — the FAR-SIDE worker our local worker.input is directed to,
//	                  set by FAR-SIDE peers (the far side decides who receives
//	                  our input, and its own PublishAllow is the boundary).
//
// Both are live, mutable, persisted bindings. A remote worker must never be free
// to directed-send into a bus it does not own: the far side grants this identity
// exactly the targets it wants reachable, enforced by its bus on every Send.
package niq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/eventbus/transport/httptrans"
	"github.com/niq-run/niq/core/impl/iface"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// Type is the worker type label: a worker whose backend is another niq.
const Type = "niq"

// Control event types. The recipient group is the runtime-routing surface: the
// SAME event sets a different binding depending on which channel it arrives on —
// this bus sets the LOCAL recipient, the far side sets the REMOTE one.
const (
	TypeRecipientSet   event.EventType = "niq.recipient.set"
	TypeRecipientUnset event.EventType = "niq.recipient.unset"
	TypeRecipientGet   event.EventType = "niq.recipient.get"

	// Service controls exposed to local peers: actively dial/redial the far side
	// and query the live bridge status.
	TypeDial   event.EventType = "niq.dial"
	TypeStatus event.EventType = "niq.status"
)

// Config is the backend's construction input: everything a declaration can say
// about the far side, plus the assembly's durable-change signal.
type Config struct {
	// ID is this worker's identity on the local bus.
	ID string
	// Recipient seeds the LOCAL recipient (which local worker receives far-side
	// messages). The live binding is a runtime property; empty means unbound.
	Recipient string
	// RemoteRecipient seeds the REMOTE recipient (which far-side worker receives
	// our local worker.input), so a deployment needs no bootstrap control event.
	RemoteRecipient string

	// RemoteURL is the base URL of the far-side bus.
	RemoteURL string
	// RemoteWorkerID is the identity this worker presents there.
	RemoteWorkerID string
	// RemoteCredential is the credential the far side issued for RemoteWorkerID.
	RemoteCredential string

	// OnDurableChange is invoked when a binding changed and must survive a
	// restart. Nil leaves signalling off.
	OnDurableChange func()
}

// ToolHost is the part of an interface worker a backend uses to serve its own
// tools. *iface.Worker satisfies it.
type ToolHost interface {
	Register(ext baseworker.Extension, h baseworker.ExtensionHandler)
	RegisterReadme(description string, text func() string, params map[string]any)
	ReplyCompleted(callerID, callID string, result any, traceID string)
	ReplyFailed(callerID, callID, errMsg, traceID string)
}

// Backend is the far side of a niq worker: the link to another niq instance.
// It implements the interfaces the interface worker looks for — SetHandler
// (injection), Start/Stop (a lifetime of its own) and Snapshot/Restore (its
// bindings are durable).
type Backend struct {
	id              string
	cfg             Config
	onDurableChange func()

	// Injected by SetHandler, before Start.
	h iface.Handler

	// dialCh wakes the link loop so a caller can force an immediate (re)dial
	// instead of waiting out the backoff.
	dialCh chan struct{}

	// target is the far-side endpoint the loop dials; mutable at runtime via
	// niq.dial so a caller can repoint the link.
	targetMu sync.RWMutex
	target   remoteTarget

	remoteMu sync.RWMutex // guards remote (nil while the far side is unreachable)
	remote   corebus.WorkerSideChannel

	muBindings  sync.RWMutex
	localRecip  string
	remoteRecip string

	mu     sync.Mutex
	cancel context.CancelFunc
}

// remoteTarget is the far-side endpoint the link dials.
type remoteTarget struct {
	url      string
	workerID string
	cred     string
}

// New builds the backend. It is pure construction: nothing dials until Start.
func New(cfg Config) *Backend {
	return &Backend{
		id:              cfg.ID,
		cfg:             cfg,
		onDurableChange: cfg.OnDurableChange,
		dialCh:          make(chan struct{}, 1),
		localRecip:      cfg.Recipient,
		remoteRecip:     cfg.RemoteRecipient,
		target: remoteTarget{
			url: cfg.RemoteURL, workerID: cfg.RemoteWorkerID, cred: cfg.RemoteCredential,
		},
	}
}

// ID is the worker id this backend belongs to.
func (b *Backend) ID() string { return b.id }

// SetHandler receives the bus handles and registers the backend's interest in
// inbound traffic. Local worker.input is the only thing worth forwarding: the
// far side receives a conversation, not this bus's lifecycle or broadcast noise.
func (b *Backend) SetHandler(h iface.Handler) {
	b.h = h
	h.OnEvent(b.onLocalInput, event.TypeWorkerInput)
}

// Start launches the far-side link. It never fails for a remote that is down:
// the worker stays locally online and cooperates while the link retries, exactly
// as if reaching the far side were "call an API".
func (b *Backend) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.cancel = cancel
	b.mu.Unlock()
	go b.runLink(runCtx)
	log.Printf("[niq %s] started; link to %s running in background", b.id, b.cfg.RemoteURL)
	return nil
}

// Stop tears the link down.
func (b *Backend) Stop() error {
	b.mu.Lock()
	cancel := b.cancel
	b.cancel = nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if r := b.remoteNow(); r != nil {
		_ = r.Close()
		b.setRemote(nil)
	}
	return nil
}

// ── Durable state ──

type bindingsState struct {
	Local  string `json:"local_recipient,omitempty"`
	Remote string `json:"remote_recipient,omitempty"`
}

// Snapshot returns the recipient bindings: the only thing this backend owns that
// must survive a restart.
func (b *Backend) Snapshot() ([]byte, error) {
	l, r := b.bindings()
	return json.Marshal(bindingsState{Local: l, Remote: r})
}

// Restore replays the bindings.
func (b *Backend) Restore(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var s bindingsState
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("niq %s: restore: %w", b.id, err)
	}
	b.muBindings.Lock()
	b.localRecip = s.Local
	b.remoteRecip = s.Remote
	b.muBindings.Unlock()
	return nil
}

// ── Bindings ──

func (b *Backend) bindings() (local, remote string) {
	b.muBindings.RLock()
	defer b.muBindings.RUnlock()
	return b.localRecip, b.remoteRecip
}

func (b *Backend) setLocalRecipient(id string) {
	b.muBindings.Lock()
	b.localRecip = id
	b.muBindings.Unlock()
	b.notifyDurable()
}

func (b *Backend) setRemoteRecipient(id string) {
	b.muBindings.Lock()
	b.remoteRecip = id
	b.muBindings.Unlock()
	b.notifyDurable()
}

func (b *Backend) notifyDurable() {
	if b.onDurableChange != nil {
		b.onDurableChange()
	}
}

// ── Link ──

// runLink keeps the far-side link alive: connect → bridge → on drop, clear the
// channel and retry with backoff.
func (b *Backend) runLink(runCtx context.Context) {
	const (
		initial = time.Second
		max     = 30 * time.Second
	)
	delay := initial
	for {
		if runCtx.Err() != nil {
			return
		}
		tgt := b.currentTarget()
		remote := httptrans.NewWorkerSide(tgt.url, tgt.workerID, tgt.cred)
		connectCtx, cancel := context.WithCancel(runCtx)
		if err := remote.Connect(connectCtx, "http://remote"); err != nil {
			cancel()
			log.Printf("[niq %s] far-side bus %s unreachable (%v); retry in %s", b.id, tgt.url, err, delay)
			if !b.sleepOrDial(runCtx, delay) {
				return
			}
			delay = nextBackoff(delay, max)
			continue
		}

		remoteCh, err := remote.Receive(connectCtx)
		if err != nil {
			cancel()
			_ = remote.Close()
			log.Printf("[niq %s] far-side receive failed: %v", b.id, err)
			if !b.sleepOrDial(runCtx, delay) {
				return
			}
			delay = nextBackoff(delay, max)
			continue
		}

		b.setRemote(remote)
		delay = initial
		log.Printf("[niq %s] bridged to %s as %s", b.id, tgt.url, tgt.workerID)
		// Behave like an ordinary remote worker there: announce presence so the far
		// side's reason/directory workers can discover and call us. Best-effort —
		// the far side's PublishAllow gates it.
		b.announceRemoteNow(remote)
		b.watchRemote(connectCtx, remoteCh)

		b.setRemote(nil)
		_ = remote.Close()
		cancel()
		if runCtx.Err() != nil {
			return
		}
		if !b.sleepOrDial(runCtx, delay) {
			return
		}
	}
}

func (b *Backend) currentTarget() remoteTarget {
	b.targetMu.RLock()
	defer b.targetMu.RUnlock()
	return b.target
}

func (b *Backend) setTarget(t remoteTarget) {
	b.targetMu.Lock()
	b.target = t
	b.targetMu.Unlock()
}

// wakeDial nudges the link loop to dial immediately, coalescing bursts.
func (b *Backend) wakeDial() {
	select {
	case b.dialCh <- struct{}{}:
	default:
	}
}

// sleepOrDial sleeps d unless the context ends or a manual dial is requested.
func (b *Backend) sleepOrDial(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-b.dialCh:
		return true
	case <-ctx.Done():
		return false
	}
}

func nextBackoff(d, max time.Duration) time.Duration {
	d *= 2
	if d > max {
		return max
	}
	return d
}

func (b *Backend) setRemote(r corebus.WorkerSideChannel) {
	b.remoteMu.Lock()
	b.remote = r
	b.remoteMu.Unlock()
}

func (b *Backend) remoteNow() corebus.WorkerSideChannel {
	b.remoteMu.RLock()
	defer b.remoteMu.RUnlock()
	return b.remote
}

// ── Local side ──

// onLocalInput forwards a local worker.input to the far side's recipient — a
// FAR-SIDE worker named by the far side, never a broadcast. That bus enforces
// the remote identity's PublishAllow on the send, so a mis-granted target is
// rejected there rather than silently leaking.
func (b *Backend) onLocalInput(evt event.Event) {
	remote := b.remoteNow()
	if remote == nil {
		return
	}
	_, remoteRecip := b.bindings()
	if remoteRecip == "" {
		log.Printf("[niq %s] local input: no remote recipient set by the far side, dropping", b.id)
		return
	}
	out := event.New(event.TypeWorkerInput, b.cfg.RemoteWorkerID, evt.Payload)
	out.RequestId = evt.RequestId
	out.TraceID = evt.TraceID
	if err := remote.Send(context.Background(), out, remoteRecip); err != nil {
		log.Printf("[niq %s] send to remote recipient %s: %v", b.id, remoteRecip, err)
	}
}

// deliverToLocalRecipient hands a far-side event to the LOCAL recipient as a
// directed send — the configured receiver that distinguishes this worker from a
// broadcast relay.
//
// Only collaborative worker.input crosses. The far-side bus's lifecycle and
// broadcast traffic (worker.ready/gone/discover, reason.start/end, other
// workers' input) is not the conversation and must not be retransmitted as a
// worker.input flood.
func (b *Backend) deliverToLocalRecipient(evt event.Event) {
	if evt.Type != event.TypeWorkerInput {
		return
	}
	localRecip, _ := b.bindings()
	if localRecip == "" {
		log.Printf("[niq %s] far-side event %s: no local recipient, dropping", b.id, evt.Type)
		return
	}
	out := event.New(event.TypeWorkerInput, b.id, evt.Payload)
	out.RequestId = evt.RequestId
	out.TraceID = evt.TraceID
	out.Transient = evt.Transient
	if err := b.h.Send(context.Background(), out, localRecip); err != nil {
		log.Printf("[niq %s] deliver to recipient %s: %v", b.id, localRecip, err)
	}
}

// ── Far side ──

// watchRemote drains the far-side channel. A control event from the far side
// rebinds the REMOTE recipient (the far side deciding who on its bus receives
// our input); any other event is delivered to the LOCAL recipient.
func (b *Backend) watchRemote(ctx context.Context, remoteCh <-chan event.Event) {
	for {
		select {
		case evt, ok := <-remoteCh:
			if !ok {
				return
			}
			switch evt.Type {
			case TypeRecipientSet:
				b.handleRemoteSet(evt)
			case TypeRecipientUnset:
				b.handleRemoteUnset(evt)
			case event.TypeWorkerDiscover:
				// The far side's directory asks who is here: answer so its roster
				// sees us as a peer.
				if evt.WorkerId != "" {
					b.announceRemoteTo(evt.WorkerId)
				}
			default:
				b.deliverToLocalRecipient(evt)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (b *Backend) handleRemoteSet(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	wid := baseworker.ArgString(tc.Args, "worker_id")
	if wid == "" {
		b.replyRemote(tc, false, "worker_id is required")
		return
	}
	b.setRemoteRecipient(wid)
	b.replyRemote(tc, true, "remote recipient set to "+wid)
	log.Printf("[niq %s] remote recipient set to %s by %s", b.id, wid, tc.CallerID)
}

func (b *Backend) handleRemoteUnset(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	b.setRemoteRecipient("")
	b.replyRemote(tc, true, "remote recipient cleared")
}

// replyRemote answers a far-side control event back over the far-side channel.
func (b *Backend) replyRemote(tc baseworker.ToolCall, ok bool, msg string) {
	remote := b.remoteNow()
	if remote == nil {
		return
	}
	typ := event.TypeRequestCompleted
	key := "result"
	if !ok {
		typ = event.TypeRequestFailed
		key = "error"
	}
	out := event.New(typ, b.cfg.RemoteWorkerID, map[string]any{key: msg})
	out.RequestId = tc.CallID
	out.TraceID = tc.TraceID
	if err := remote.Send(context.Background(), out, tc.CallerID); err != nil {
		log.Printf("[niq %s] reply to far side %s: %v", b.id, tc.CallerID, err)
	}
}

// remoteReadyPayload is the worker.ready this remote identity announces on the
// far side: the controls the FAR side may use on us.
func (b *Backend) remoteReadyPayload() map[string]any {
	return map[string]any{
		"type": Type,
		"watch": []map[string]any{
			{"event": string(TypeRecipientSet), "desc": "Bind which of our workers receives the far side's input.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"worker_id": map[string]any{"type": "string", "description": "Far-side worker id to bind as the remote recipient."},
					},
					"required": []any{"worker_id"},
				},
			},
			{"event": string(TypeRecipientUnset), "desc": "Clear who receives the far side's input."},
		},
	}
}

// announceRemoteNow broadcasts this worker's presence on the far side. Best
// effort: the far side's PublishAllow for this identity gates it, which is the
// "far side controls what a remote worker may publish" model.
func (b *Backend) announceRemoteNow(remote corebus.WorkerSideChannel) {
	out := event.New(event.TypeWorkerReady, b.cfg.RemoteWorkerID, b.remoteReadyPayload())
	out.ExcludeWorkerID = b.cfg.RemoteWorkerID
	out.Transient = true
	if err := remote.Broadcast(context.Background(), out); err != nil {
		log.Printf("[niq %s] announce ready on far side %s: %v", b.id, b.cfg.RemoteURL, err)
	}
	log.Printf("[niq %s] announced ready on far side as %s", b.id, b.cfg.RemoteWorkerID)
}

// announceRemoteTo replies to a far-side worker.discover by sending our ready
// directly to the asker.
func (b *Backend) announceRemoteTo(target string) {
	remote := b.remoteNow()
	if remote == nil || target == "" {
		return
	}
	out := event.New(event.TypeWorkerReady, b.cfg.RemoteWorkerID, b.remoteReadyPayload())
	out.Transient = true
	if err := remote.Send(context.Background(), out, target); err != nil {
		log.Printf("[niq %s] discover reply to %s: %v", b.id, target, err)
	}
}

// ── Tools ──

// RegisterTools registers this backend's tools on the interface worker that
// hosts it. It is the builder that calls this, because it is the one that knows
// both halves.
func (b *Backend) RegisterTools(host ToolHost) {
	host.Register(baseworker.Extension{
		Event:       TypeRecipientSet,
		Response:    baseworker.StringSchema("confirmation naming the local recipient"),
		Description: "Bind which local worker receives the far side's messages.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"worker_id": map[string]any{"type": "string", "description": "Local worker id to bind as the recipient."},
			},
			"required": []any{"worker_id"},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		wid := baseworker.ArgString(tc.Args, "worker_id")
		if wid == "" {
			host.ReplyFailed(tc.CallerID, tc.CallID, "worker_id is required", tc.TraceID)
			return
		}
		b.setLocalRecipient(wid)
		host.ReplyCompleted(tc.CallerID, tc.CallID, "local recipient set to "+wid, tc.TraceID)
		log.Printf("[niq %s] local recipient set to %s by %s", b.id, wid, tc.CallerID)
	})

	host.Register(baseworker.Extension{
		Event:       TypeRecipientUnset,
		Response:    baseworker.StringSchema("confirmation"),
		Description: "Clear the local recipient: stop delivering far-side messages locally.",
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		b.setLocalRecipient("")
		host.ReplyCompleted(tc.CallerID, tc.CallID, "local recipient cleared", tc.TraceID)
	})

	host.Register(baseworker.Extension{
		Event:       TypeRecipientGet,
		Response:    baseworker.ObjectSchema(map[string]any{"local": map[string]any{"type": "string"}, "remote": map[string]any{"type": "string"}}),
		Description: "Return the current local/remote recipient bindings as JSON.",
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		l, r := b.bindings()
		host.ReplyCompleted(tc.CallerID, tc.CallID, map[string]any{"local": l, "remote": r}, tc.TraceID)
	})

	host.Register(baseworker.Extension{
		Event:       TypeDial,
		Response:    baseworker.StringSchema("connection outcome"),
		Description: "Actively connect/reconnect the link to the far side. Optionally repoint it with remote_url / remote_worker_id / remote_credential.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"remote_url":        map[string]any{"type": "string", "description": "(Optional) Repoint the far-side bus URL."},
				"remote_worker_id":  map[string]any{"type": "string", "description": "(Optional) Repoint the identity presented on the far side."},
				"remote_credential": map[string]any{"type": "string", "description": "(Optional) Repoint the far-side credential."},
			},
		},
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		b.handleDial(tc, host)
	})

	host.Register(baseworker.Extension{
		Event:       TypeStatus,
		Response:    baseworker.ObjectSchema(map[string]any{"connected": map[string]any{"type": "boolean"}, "remote_url": map[string]any{"type": "string"}, "remote_worker_id": map[string]any{"type": "string"}, "remote_cred_set": map[string]any{"type": "boolean"}, "local_recipient": map[string]any{"type": "string"}, "remote_recipient": map[string]any{"type": "string"}}),
		Description: "Return the current link status: connected, far-side target, and recipient bindings.",
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		tgt := b.currentTarget()
		l, r := b.bindings()
		host.ReplyCompleted(tc.CallerID, tc.CallID, statusState{
			Connected:       b.remoteNow() != nil,
			RemoteURL:       tgt.url,
			RemoteWorkerID:  tgt.workerID,
			RemoteCredSet:   tgt.cred != "",
			LocalRecipient:  l,
			RemoteRecipient: r,
		}, tc.TraceID)
	})

	host.RegisterReadme(readmeDescription, b.readmeText, nil)
}

// statusState is the live link status served by niq.status.
type statusState struct {
	Connected       bool   `json:"connected"`
	RemoteURL       string `json:"remote_url,omitempty"`
	RemoteWorkerID  string `json:"remote_worker_id,omitempty"`
	RemoteCredSet   bool   `json:"remote_credential_set"`
	LocalRecipient  string `json:"local_recipient,omitempty"`
	RemoteRecipient string `json:"remote_recipient,omitempty"`
}

// handleDial actively (re)establishes the far-side link.
func (b *Backend) handleDial(tc baseworker.ToolCall, host ToolHost) {
	url, _ := tc.Args["remote_url"].(string)
	workerID, _ := tc.Args["remote_worker_id"].(string)
	cred, _ := tc.Args["remote_credential"].(string)
	repoint := url != "" || workerID != ""

	tgt := b.currentTarget()
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
		b.setTarget(tgt)
	}

	connected := b.remoteNow() != nil
	b.wakeDial()
	// Repointing while connected: tear the current link down so the loop
	// re-establishes against the new target instead of leaving the old one up.
	if repoint && connected {
		if r := b.remoteNow(); r != nil {
			_ = r.Close()
		}
		b.setRemote(nil)
		connected = false
	}

	if connected {
		host.ReplyCompleted(tc.CallerID, tc.CallID, "already connected to "+tgt.url, tc.TraceID)
		return
	}
	host.ReplyCompleted(tc.CallerID, tc.CallID, "dialing "+tgt.url, tc.TraceID)
	log.Printf("[niq %s] manual dial requested by %s -> %s", b.id, tc.CallerID, tgt.url)
}

// readmeDescription is the one-line presence text for worker.readme.
const readmeDescription = "A niq worker: it links this bus to another niq instance and relays the " +
	"conversation between one local worker and one far-side worker."
