//go:build unix

package helper

import (
	"net"
	"os"
	"os/exec"
	"testing"
)

// TestHoldersFindsListener pins the one property the CLI needs: the process
// holding a port is discoverable, so it can be shown to the operator before
// anything is signalled.
func TestHoldersFindsListener(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	holders, err := Holders(port)
	if err != nil {
		t.Fatalf("Holders(%d): %v", port, err)
	}
	for _, h := range holders {
		if h.PID == os.Getpid() {
			return
		}
	}
	t.Fatalf("Holders(%d) = %+v, want pid %d", port, holders, os.Getpid())
}

// TestParseHolders covers lsof's field output without needing lsof: one p line
// opens a process, the following c line names it, and pid 1 is dropped.
func TestParseHolders(t *testing.T) {
	got := parseHolders("p123\ncnginx\np4500\ncnode\np1\ncinit\n")
	want := []Holder{{PID: 123, Command: "nginx"}, {PID: 4500, Command: "node"}}
	if len(got) != len(want) {
		t.Fatalf("parseHolders = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseHolders[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestSignalNeverTargetsSelf guards the CLI's own process: freeing a taken port
// must not take the front-end down with it. Reaching the assertion at all is
// the proof — a delivery of os.Interrupt would end the test process.
func TestSignalNeverTargetsSelf(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if n := Signal(port, os.Interrupt); n != 0 {
		t.Fatalf("Signal on our own listener signalled %d process(es)", n)
	}
}
