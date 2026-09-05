package eventbus

import (
	"path/filepath"
	"testing"

	corebus "github.com/niq-run/niq/core/bus"
)

// closeRecvChannel is a recvChannel that records whether Close was called, so a
// reconnect test can assert the replaced (stale) connection is torn down.
type closeRecvChannel struct {
	recvChannel
	closed bool
}

func (c *closeRecvChannel) Close() error {
	c.closed = true
	return nil
}

// TestConnectReplacesStaleConnection verifies a reconnect for the same worker id
// replaces the existing (stale) channel instead of failing with "already
// connected" — the case a supervisor-restarted external worker hits when it
// reconnects before the old connection is torn down. The old channel must be
// closed so its watch goroutine exits.
func TestConnectReplacesStaleConnection(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := registry.Register(corebus.Identity{WorkerID: "srv", Type: "x"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	engine := NewEngine(registry, nil)
	old := &closeRecvChannel{recvChannel: recvChannel{id: "c1", worker: "srv"}}
	if err := engine.Connect("srv", old); err != nil {
		t.Fatalf("first Connect: %v", err)
	}

	// Reconnect with a fresh channel for the same id: must succeed and replace
	// the old channel, and close the stale one.
	nw := &closeRecvChannel{recvChannel: recvChannel{id: "c2", worker: "srv"}}
	if err := engine.Connect("srv", nw); err != nil {
		t.Fatalf("reconnect should replace the stale channel, got %v", err)
	}
	if engine.Channel("srv") != nw {
		t.Fatalf("engine channel not replaced: got %+v, want new channel", engine.Channel("srv"))
	}
	if !old.closed {
		t.Fatal("stale channel was not closed on replacement")
	}
	if nw.closed {
		t.Fatal("new channel must not be closed")
	}
}

// TestDisconnectChannelOnlyRemovesOwnedConnection verifies DisconnectChannel
// does not remove a worker's connection when it has already been replaced by a
// newer one — so a late-arriving old connection's teardown never knocks out the
// live replacement.
func TestDisconnectChannelOnlyRemovesOwnedConnection(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := registry.Register(corebus.Identity{WorkerID: "srv", Type: "x"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	engine := NewEngine(registry, nil)
	old := &recvChannel{id: "c1", worker: "srv"}
	if err := engine.Connect("srv", old); err != nil {
		t.Fatalf("Connect old: %v", err)
	}
	nw := &recvChannel{id: "c2", worker: "srv"}
	if err := engine.Connect("srv", nw); err != nil {
		t.Fatalf("Connect new: %v", err)
	}

	// The old connection tries to detach itself; it must be a no-op because the
	// live connection is now the replacement.
	engine.DisconnectChannel("srv", old)
	if engine.Channel("srv") != nw {
		t.Fatal("DisconnectChannel of a stale channel removed the live replacement")
	}

	// The live connection's own teardown still detaches it.
	engine.DisconnectChannel("srv", nw)
	if engine.Channel("srv") != nil {
		t.Fatal("DisconnectChannel of the live channel did not detach the worker")
	}
}
