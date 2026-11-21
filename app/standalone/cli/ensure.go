// Port-conflict resolution, done before the gateway daemon is spawned.
//
// The gateway's job is to bind and, when the address is taken, say so
// (gateway.PortInUseError). What to do about it is a lifecycle decision that
// needs a terminal, so it lives here — in the short-lived front-end — not in
// the gateway (which runs detached, with no stdin) and not in a UI client
// (which only exists once a gateway is already serving).
//
// The operator gets the choice the situation calls for: stop whatever holds
// the address, or start on another port. With no terminal (a script, or the
// detached child `w --foreground` that startBackground spawns) there is nobody
// to ask, so the conflict becomes an actionable error instead of a prompt.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/niq-run/niq/app/standalone/gateway"
	"github.com/niq-run/niq/app/standalone/helper"
)

// portConflict is what the front-end knows when the address is taken.
type portConflict struct {
	Addr    string
	Port    int
	Holders []helper.Holder // best effort; empty when discovery is unavailable
}

// conflictChoice is the operator's answer.
type conflictChoice int

const (
	conflictCancel conflictChoice = iota
	conflictKill
	conflictAnotherPort
)

// askConflict presents the choice and returns it. For conflictAnotherPort the
// second value is the address to use. A nil ask means nobody can be asked.
type askConflict func(portConflict) (conflictChoice, string, error)

// portOps is the seam the resolution talks through, so the decision is
// testable without a real socket or a real process to signal.
type portOps struct {
	probe   func(addr string) error
	holders func(port int) ([]helper.Holder, error)
	kill    func(port int, grace time.Duration) (int, error)
}

var defaultPortOps = portOps{
	probe:   helper.ProbeListen,
	holders: helper.Holders,
	kill:    helper.Kill,
}

// ensureGateway makes sure a background gateway is serving. It resolves an
// occupied address through ask before spawning the child. (nil, nil) means
// there was nothing to do: the gateway was already running, or the operator
// cancelled.
func ensureGateway(addr, auth string, ask askConflict) (*shellState, error) {
	if st, ok := readShellState(); ok && isRunning(st) {
		return nil, reportAlreadyRunning(st, addr)
	}
	newAddr, cancelled, err := resolvePortConflict(addr, ask, defaultPortOps)
	if err != nil || cancelled {
		return nil, err
	}
	return startBackground(newAddr, auth)
}

// resolvePortConflict returns the address to start on. cancelled is true when
// the operator declined to resolve the conflict, and the caller should exit
// quietly rather than report a failure.
//
// A dynamic port (":0", "127.0.0.1:0") cannot conflict, so it passes straight
// through and the OS picks one.
func resolvePortConflict(addr string, ask askConflict, ops portOps) (string, bool, error) {
	if helper.Port(addr) == 0 {
		return addr, false, nil
	}
	err := ops.probe(addr)
	if err == nil {
		return addr, false, nil
	}
	if !gateway.IsPortInUse(err) {
		return "", false, fmt.Errorf("listen on %s: %w", addr, err)
	}

	port := helper.Port(addr)
	holders, _ := ops.holders(port) // best effort: the error works without it
	if ask == nil {
		return "", false, conflictError(addr, holders)
	}
	choice, newAddr, err := ask(portConflict{Addr: addr, Port: port, Holders: holders})
	if err != nil {
		return "", false, err
	}
	switch choice {
	case conflictKill:
		if _, err := ops.kill(port, 3*time.Second); err != nil {
			if errors.Is(err, helper.ErrUnsupported) {
				return "", false, fmt.Errorf("%s is in use and niq cannot stop the holder on this platform: pass --addr with another port", addr)
			}
			return "", false, fmt.Errorf("stop the process on %s: %w", addr, err)
		}
		if err := ops.probe(addr); err != nil {
			return "", false, fmt.Errorf("stopped the process on %s but the address is still in use: %w", addr, err)
		}
		return addr, false, nil
	case conflictAnotherPort:
		if newAddr == "" {
			return "", false, errors.New("no alternative address was chosen")
		}
		return newAddr, false, nil
	default:
		return "", true, nil
	}
}

// conflictError is the non-interactive answer: say what holds the address and
// give the two ways out, so a script or the detached child still reports
// something actionable.
func conflictError(addr string, holders []helper.Holder) error {
	msg := addr + " is already in use"
	if s := holderSummary(holders); s != "" {
		msg += " by " + s
	}
	msg += ": stop it (a niq gateway stops with `niq stop`) or start on another port with --addr"
	return errors.New(msg)
}

// holderSummary renders "pid 123 (nginx), pid 456 (node)".
func holderSummary(holders []helper.Holder) string {
	parts := make([]string, 0, len(holders))
	for _, h := range holders {
		if h.Command != "" {
			parts = append(parts, fmt.Sprintf("pid %d (%s)", h.PID, h.Command))
			continue
		}
		parts = append(parts, fmt.Sprintf("pid %d", h.PID))
	}
	return strings.Join(parts, ", ")
}

// askOnTerminal returns the prompt when stdin is a terminal, and nil otherwise
// so scripts and the detached child never block on a question.
func askOnTerminal() askConflict {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	return promptConflict
}

// promptConflict asks the operator what to do about a taken address. It is a
// plain line prompt rather than a full-screen menu: it runs before the gateway
// exists, so there is no UI to speak of yet.
func promptConflict(c portConflict) (conflictChoice, string, error) {
	head := fmt.Sprintf("%s %s", red("in use"), bold(c.Addr))
	if s := holderSummary(c.Holders); s != "" {
		head += " " + dim("held by "+s)
	}
	fmt.Printf("\n%s\n", head)
	fmt.Printf("  %s %s\n", bold("[k]"), dim("stop it and use this address"))
	fmt.Printf("  %s %s\n", bold("[n]"), dim("start on another port"))
	fmt.Printf("  %s %s\n", bold("[c]"), dim("cancel"))

	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print(dim("choice [k/n/c]: "))
		line, err := in.ReadString('\n')
		if err != nil {
			return conflictCancel, "", nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "k", "kill":
			return conflictKill, c.Addr, nil
		case "n", "new", "port":
			addr, err := promptPort(c, in)
			if err != nil {
				return conflictCancel, "", err
			}
			return conflictAnotherPort, addr, nil
		case "c", "cancel", "q":
			return conflictCancel, "", nil
		}
	}
}

// promptPort offers the next free port next to the conflicting one and accepts
// an explicit replacement. It re-probes before returning, so a bad or taken
// choice loops instead of failing after the daemon was already spawned.
func promptPort(c portConflict, in *bufio.Reader) (string, error) {
	suggest, _ := nextFreeAddr(c.Addr, defaultPortOps)
	for {
		if suggest != "" {
			fmt.Printf(dim("port [%s]: "), strconv.Itoa(helper.Port(suggest)))
		} else {
			fmt.Print(dim("port: "))
		}
		line, err := in.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" && suggest != "" {
			return suggest, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > 65535 {
			fmt.Println(red("not a port number"))
			continue
		}
		host, _, splitErr := net.SplitHostPort(c.Addr)
		if splitErr != nil {
			return "", splitErr
		}
		addr := net.JoinHostPort(host, strconv.Itoa(n))
		if err := defaultPortOps.probe(addr); err != nil {
			fmt.Println(red(addr + " is not available"))
			continue
		}
		return addr, nil
	}
}

// nextFreeAddr proposes the first bindable port after the one that clashed,
// on the same host, so "another port" keeps the same exposure.
func nextFreeAddr(addr string, ops portOps) (string, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	start, err := strconv.Atoi(portStr)
	if err != nil || start <= 0 {
		return "", fmt.Errorf("no port in %s", addr)
	}
	for p := start + 1; p <= start+100 && p <= 65535; p++ {
		candidate := net.JoinHostPort(host, strconv.Itoa(p))
		if ops.probe(candidate) == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free port near %s", addr)
}
