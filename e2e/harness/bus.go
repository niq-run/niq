package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/eventbus/transport/httptrans"
	"github.com/niq-run/niq/core/itfs/event"
)

// BusPeer is an external participant on a project's bus: a worker the host did
// not launch, dialing in with the credential it was issued. It is the same path
// a third-party agent or the TypeScript SDK takes, so a test that uses it is
// exercising the real transport, ACL and routing rather than a shortcut.
type BusPeer struct {
	ID         string
	Credential string

	ws   *httptrans.WorkerSide
	base baseworker.BaseWorker

	mu     sync.Mutex
	seen   []event.Event
	cancel context.CancelFunc
}

// DialIn is a declared dial-in worker that has not connected yet: the host has
// minted its identity and credential, but the bus only knows it once Connect
// attaches a channel.
type DialIn struct {
	ID         string
	Credential string
	BusURL     string
}

// DeclareDialIn declares a dial-in worker without connecting. Placement
// unmanaged-process tells the host "someone else runs this"; the gateway reads
// the credential back from the bus identity (never from the host's reply, which
// carries no secrets).
func (g *Gateway) DeclareDialIn(t *testing.T, projectID, peerID string) *DialIn {
	t.Helper()
	row, ok := g.Project(t, projectID)
	if !ok || row.Bus == 0 {
		t.Fatalf("project %q has no bus port (ok=%v, row=%+v)", projectID, ok, row)
	}

	code, body := g.PostJSON(t, "/p/"+projectID+"/api/workers/create", map[string]string{
		"id":        peerID,
		"type":      "e2e-peer",
		"placement": "unmanaged-process",
	})
	if code != http.StatusOK {
		t.Fatalf("declare dial-in worker %q: %d %s", peerID, code, body)
	}
	var created struct {
		ID         string `json:"id"`
		Remote     bool   `json:"remote"`
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode worker/create: %v\n%s", err, body)
	}
	if !created.Remote || created.Credential == "" {
		t.Fatalf("declared worker is not a dial-in one: %s", body)
	}
	return &DialIn{
		ID:         created.ID,
		Credential: created.Credential,
		BusURL:     fmt.Sprintf("http://127.0.0.1:%d", row.Bus),
	}
}

// Connect attaches to the bus as the declared worker. A bad credential is a
// failed test: use TryConnect when the failure is the assertion.
func (d *DialIn) Connect(t *testing.T) *BusPeer {
	t.Helper()
	p, err := d.TryConnect(t)
	if err != nil {
		t.Fatalf("connect %q to %s: %v", d.ID, d.BusURL, err)
	}
	return p
}

// TryConnect attaches to the bus and returns the transport's error instead of
// failing: the bus validates the credential when the stream opens.
func (d *DialIn) TryConnect(t *testing.T) (*BusPeer, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	ws := httptrans.NewWorkerSide(d.BusURL, d.ID, d.Credential)
	if err := ws.Connect(ctx, d.BusURL); err != nil {
		cancel()
		return nil, err
	}
	p := &BusPeer{
		ID:         d.ID,
		Credential: d.Credential,
		ws:         ws,
		base:       baseworker.NewBaseWorker(d.ID, ws),
		cancel:     cancel,
	}

	ch, err := ws.Receive(ctx)
	if err != nil {
		cancel()
		_ = ws.Close()
		return nil, err
	}
	go func() {
		for {
			select {
			case evt, ok := <-ch:
				if !ok {
					return
				}
				p.mu.Lock()
				p.seen = append(p.seen, evt)
				p.mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = ws.Close()
	})
	return p, nil
}

// DialBus declares and immediately connects a peer: the common case.
func (g *Gateway) DialBus(t *testing.T, projectID, peerID string) *BusPeer {
	t.Helper()
	return g.DeclareDialIn(t, projectID, peerID).Connect(t)
}

// Close detaches from the bus. The peer is not reusable: connect again with
// the same DialIn, which is what a reconnecting participant does.
func (p *BusPeer) Close() {
	p.cancel()
	_ = p.ws.Close()
}

// Base is the embedded worker helper, for peers that answer requests.
func (p *BusPeer) Base() *baseworker.BaseWorker { return &p.base }

// Send delivers evt to the named targets only.
func (p *BusPeer) Send(t *testing.T, evt event.Event, targets ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.ws.Send(ctx, evt, targets...)
}

// Broadcast publishes evt to everyone whose grants match its type.
func (p *BusPeer) Broadcast(t *testing.T, evt event.Event) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.ws.Broadcast(ctx, evt)
}

// Received is what this peer has been delivered so far.
func (p *BusPeer) Received() []event.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]event.Event(nil), p.seen...)
}

// Await waits for a delivered event that matches pred and returns it. Assertions
// about absence use Received, not Await: waiting for something that must not
// happen can only time out.
func (p *BusPeer) Await(t *testing.T, what string, timeout time.Duration, pred func(event.Event) bool) event.Event {
	t.Helper()
	var found event.Event
	WaitFor(t, what, timeout, func() bool {
		for _, evt := range p.Received() {
			if pred(evt) {
				found = evt
				return true
			}
		}
		return false
	})
	return found
}

// NewEvent builds an event the way a worker does: this peer is the source.
func NewEvent(peerID string, typ string, payload map[string]any) event.Event {
	return event.New(event.EventType(typ), peerID, payload)
}
