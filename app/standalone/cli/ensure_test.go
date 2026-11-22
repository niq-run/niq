package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/app/standalone/gateway"
	"github.com/niq-run/niq/app/standalone/helper"
)

// fakeOps records what the resolution asked for and answers with scripted
// results, so every branch is covered without a socket or a real process.
type fakeOps struct {
	probe      func(addr string, call int) error
	probeCalls int
	holders    []helper.Holder
	holdersErr error
	killErr    error
	killCalls  int
}

func (f *fakeOps) build() portOps {
	return portOps{
		probe: func(addr string) error {
			f.probeCalls++
			return f.probe(addr, f.probeCalls)
		},
		holders: func(int) ([]helper.Holder, error) { return f.holders, f.holdersErr },
		kill: func(int, time.Duration) (int, error) {
			f.killCalls++
			return 1, f.killErr
		},
	}
}

func busy(string, int) error { return &gateway.PortInUseError{Addr: "127.0.0.1:9527"} }

func TestResolvePortConflictFree(t *testing.T) {
	f := &fakeOps{probe: func(string, int) error { return nil }}
	addr, cancelled, err := resolvePortConflict("127.0.0.1:9527", nil, f.build())
	if err != nil || cancelled || addr != "127.0.0.1:9527" {
		t.Fatalf("free port = (%q,%v,%v), want the address unchanged", addr, cancelled, err)
	}
	if f.probeCalls != 1 {
		t.Fatalf("probe calls = %d, want 1", f.probeCalls)
	}
}

// A dynamic port cannot conflict: the OS assigns one, so probing it would bind
// and release a port for no reason.
func TestResolvePortConflictDynamicSkipsProbe(t *testing.T) {
	f := &fakeOps{probe: func(string, int) error { return errors.New("boom") }}
	addr, cancelled, err := resolvePortConflict("127.0.0.1:0", nil, f.build())
	if err != nil || cancelled || addr != "127.0.0.1:0" {
		t.Fatalf("dynamic port = (%q,%v,%v)", addr, cancelled, err)
	}
	if f.probeCalls != 0 {
		t.Fatalf("probe calls = %d, want 0", f.probeCalls)
	}
}

// A bind failure that is not "in use" is not something stopping a process can
// fix, so it must surface unchanged rather than prompt.
func TestResolvePortConflictOtherError(t *testing.T) {
	f := &fakeOps{probe: func(string, int) error { return errors.New("permission denied") }}
	_, _, err := resolvePortConflict("127.0.0.1:9527", nil, f.build())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want the bind error", err)
	}
}

func TestResolvePortConflictNoTerminalIsActionable(t *testing.T) {
	f := &fakeOps{
		probe:   busy,
		holders: []helper.Holder{{PID: 123, Command: "nginx"}},
	}
	_, _, err := resolvePortConflict("127.0.0.1:9527", nil, f.build())
	if err == nil {
		t.Fatal("expected an error without a prompt")
	}
	for _, want := range []string{"already in use", "pid 123 (nginx)", "another port"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to mention %q", err, want)
		}
	}
}

func TestResolvePortConflictCancel(t *testing.T) {
	f := &fakeOps{probe: busy}
	ask := func(portConflict) (conflictChoice, string, error) { return conflictCancel, "", nil }
	if _, cancelled, err := resolvePortConflict("127.0.0.1:9527", ask, f.build()); err != nil || !cancelled {
		t.Fatalf("cancel = (%v,%v), want cancelled with no error", cancelled, err)
	}
}

func TestResolvePortConflictKill(t *testing.T) {
	f := &fakeOps{probe: func(_ string, call int) error {
		if call == 1 {
			return busy("", 0)
		}
		return nil
	}}
	ask := func(portConflict) (conflictChoice, string, error) { return conflictKill, "", nil }
	addr, cancelled, err := resolvePortConflict("127.0.0.1:9527", ask, f.build())
	if err != nil || cancelled || addr != "127.0.0.1:9527" {
		t.Fatalf("kill = (%q,%v,%v), want the same address", addr, cancelled, err)
	}
	if f.killCalls != 1 {
		t.Fatalf("kill calls = %d, want 1", f.killCalls)
	}
	if f.probeCalls != 2 {
		t.Fatalf("probe calls = %d, want the pre-flight and the re-check", f.probeCalls)
	}
}

func TestResolvePortConflictKillLeavesAddressBusy(t *testing.T) {
	f := &fakeOps{probe: busy}
	ask := func(portConflict) (conflictChoice, string, error) { return conflictKill, "", nil }
	_, _, err := resolvePortConflict("127.0.0.1:9527", ask, f.build())
	if err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Fatalf("err = %v, want a still-in-use error", err)
	}
}

func TestResolvePortConflictKillUnsupported(t *testing.T) {
	f := &fakeOps{probe: busy, killErr: helper.ErrUnsupported}
	ask := func(portConflict) (conflictChoice, string, error) { return conflictKill, "", nil }
	_, _, err := resolvePortConflict("127.0.0.1:9527", ask, f.build())
	if err == nil || !strings.Contains(err.Error(), "cannot stop the holder") {
		t.Fatalf("err = %v, want the unsupported-platform hint", err)
	}
}

func TestResolvePortConflictAnotherPort(t *testing.T) {
	f := &fakeOps{probe: busy}
	ask := func(portConflict) (conflictChoice, string, error) {
		return conflictAnotherPort, "127.0.0.1:9528", nil
	}
	addr, cancelled, err := resolvePortConflict("127.0.0.1:9527", ask, f.build())
	if err != nil || cancelled || addr != "127.0.0.1:9528" {
		t.Fatalf("another port = (%q,%v,%v)", addr, cancelled, err)
	}
}

func TestResolvePortConflictAnotherPortEmpty(t *testing.T) {
	f := &fakeOps{probe: busy}
	ask := func(portConflict) (conflictChoice, string, error) { return conflictAnotherPort, "", nil }
	if _, _, err := resolvePortConflict("127.0.0.1:9527", ask, f.build()); err == nil {
		t.Fatal("expected an error when no alternative address was chosen")
	}
}

// TestNextFreeAddrKeepsTheHost is the property behind "start on another port":
// the suggestion must not widen (or narrow) the exposure the user asked for.
func TestNextFreeAddrKeepsTheHost(t *testing.T) {
	f := &fakeOps{probe: func(addr string, _ int) error {
		if strings.HasSuffix(addr, ":9529") {
			return nil
		}
		return &gateway.PortInUseError{Addr: addr}
	}}
	got, err := nextFreeAddr("127.0.0.1:9527", f.build())
	if err != nil {
		t.Fatalf("nextFreeAddr: %v", err)
	}
	if got != "127.0.0.1:9529" {
		t.Fatalf("nextFreeAddr = %q, want 127.0.0.1:9529", got)
	}
}
