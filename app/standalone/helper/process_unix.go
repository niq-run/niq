//go:build unix

package helper

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// lsofPath is resolved once. Empty means discovery is unavailable here (a
// minimal container without lsof), which is reported as ErrUnsupported.
var lsofPath = lookupLsof()

func lookupLsof() string {
	p, err := exec.LookPath("lsof")
	if err != nil {
		return ""
	}
	return p
}

// Holders lists the processes LISTENING on port. A client merely connected to
// that port is not included: only listeners own the bind, and signalling a
// client (the gateway proxying a project, say) would be a bug.
func Holders(port int) ([]Holder, error) {
	if port <= 0 || lsofPath == "" {
		return nil, ErrUnsupported
	}
	out, err := exec.Command(lsofPath, "-Fpc", "-i", "tcp:"+strconv.Itoa(port), "-sTCP:LISTEN").Output()
	if err != nil {
		// lsof exits 1 when nothing matches; that is "no holders", not a
		// failure, so report it the same way as an empty listing.
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, nil
		}
		return nil, err
	}
	return parseHolders(string(out)), nil
}

// parseHolders reads lsof's -F output: "p<pid>" starts a process group and
// "c<command>" names it, so each p line begins one Holder.
func parseHolders(out string) []Holder {
	var holders []Holder
	current := -1
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, err := strconv.Atoi(line[1:])
			if err != nil || pid <= 1 { // never signal init/pid 1
				current = -1
				continue
			}
			holders = append(holders, Holder{PID: pid})
			current = len(holders) - 1
		case 'c':
			if current >= 0 {
				holders[current].Command = line[1:]
			}
		}
	}
	return holders
}

// Signal sends sig to every listener on port and returns how many were
// signalled. This process is never signalled: the caller asking to free a port
// must not take itself down.
// Alive reports whether pid still exists. Signal 0 is the probe: ESRCH means
// gone, EPERM means alive but owned by someone else. Init is never reported
// alive, matching Holders.
func Alive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func Signal(port int, sig os.Signal) int {
	holders, err := Holders(port)
	if err != nil {
		return 0
	}
	return signalAll(holders, sig)
}

func signalAll(holders []Holder, sig os.Signal) int {
	self := os.Getpid()
	n := 0
	for _, h := range holders {
		if h.PID <= 1 || h.PID == self {
			continue
		}
		p, err := os.FindProcess(h.PID)
		if err != nil {
			continue
		}
		if p.Signal(sig) == nil {
			n++
		}
	}
	return n
}

// Kill stops the listeners on port: SIGTERM first, then SIGKILL for whatever
// is still listening once grace has elapsed. grace <= 0 skips the wait.
// Returns how many processes were signalled in total.
func Kill(port int, grace time.Duration) (int, error) {
	holders, err := Holders(port)
	if err != nil {
		return 0, err
	}
	n := signalAll(holders, syscall.SIGTERM)
	if n == 0 {
		// Nothing left to signal: it exited between the probe and now.
		return 0, nil
	}
	deadline := time.Now().Add(grace)
	for {
		left, err := Holders(port)
		if err != nil || len(left) == 0 {
			return n, nil
		}
		if grace <= 0 || !time.Now().Before(deadline) {
			return n + signalAll(left, syscall.SIGKILL), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}
