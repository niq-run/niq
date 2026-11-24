// The start address: the remembered bind preference and the one-time question
// asked before the gateway starts.
//
// Binding is the gateway's job, but *whether* to expose the plane to other
// machines is a lifecycle decision that needs a terminal. The daemon start path
// runs detached with the null device as stdio (see cli/README.md), so the
// question lives here in the short-lived front-end, exactly like the
// port-conflict choice in ensure.go.
//
// Resolution order: an explicit --addr wins, then the remembered preference,
// then the loopback default. An absent preference is not "loopback" — it means
// the operator has not decided, so a start on a terminal asks, and offers to
// remember the answer so it stops asking.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/niq-run/niq/app/standalone/gateway"
	"github.com/niq-run/niq/app/standalone/helper"
	"github.com/niq-run/niq/app/standalone/store"
)

// startPref is the persisted start preference, <root>/common/config.json. An
// empty DefaultAddr means "not decided": the next interactive start asks again.
//
// It is deliberately not shell.json — that record is the live pid/bound
// address, cleared on stop, and a stale record is treated as "not running".
// This file outlives any single run.
type startPref struct {
	DefaultAddr string `json:"default_addr,omitempty"`
}

// readStartPref loads the preference. A missing or garbled file reads as unset
// rather than as an error: a hand-edited config must never block a start.
func readStartPref() startPref {
	var p startPref
	b, err := os.ReadFile(store.ConfigPath())
	if err != nil {
		return p
	}
	_ = json.Unmarshal(b, &p)
	return p
}

// writeStartPref replaces the preference file through a temp file and a
// rename, so a reader never sees a half-written record (same discipline as
// shell.json).
func writeStartPref(p startPref) error {
	path := store.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// savedStartAddr reports the remembered bind address, if one was decided.
func savedStartAddr() (string, bool) {
	if a := strings.TrimSpace(readStartPref().DefaultAddr); a != "" {
		return a, true
	}
	return "", false
}

// rememberStartAddr persists addr as the default for future starts, preserving
// any other keys the file may carry.
func rememberStartAddr(addr string) error {
	p := readStartPref()
	p.DefaultAddr = addr
	return writeStartPref(p)
}

// authConfigured reports whether basic-auth credentials exist on disk. A remote
// bind without them serves anyone who can reach the port, so both the prompt
// and the resolver refuse to take such a bind.
func authConfigured() bool {
	cfg, err := gateway.LoadAuth(store.AuthConfigPath())
	return err == nil && cfg.User != "" && cfg.Pass != ""
}

// bindChoice is the operator's answer to "who may reach the gateway?".
type bindChoice struct {
	Addr     string // address to bind for this start
	Remember bool   // persist Addr so future starts stop asking
}

// askBind presents the choice and its follow-up ("remember this?"). A nil ask
// means nobody can be asked, so the default applies.
type askBind func(current string, auth bool) (bindChoice, error)

// askOnTerminalBind returns the prompt when stdin is a terminal, and nil
// otherwise so a script (or the detached child) never blocks on a question.
func askOnTerminalBind() askBind {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	return func(current string, auth bool) (bindChoice, error) {
		return promptBind(current, auth, in)
	}
}

// startRequest is what the command line asked for, before any default applies.
// Zero value = "say nothing", which is what keeps the remembered preference and
// the interactive question in play.
type startRequest struct {
	Addr   string // --addr, empty when not given
	Remote bool   // --remote: all interfaces, this run
	Local  bool   // --local: loopback only, this run (overrides a saved remote)
}

// validate rejects contradictory combinations before anything is resolved: the
// bind is a single decision, so naming it twice in different ways is a mistake
// the caller should see rather than have silently resolved.
func (r startRequest) validate() error {
	if r.Remote && r.Local {
		return errors.New("--remote and --local are mutually exclusive")
	}
	if r.Addr != "" && (r.Remote || r.Local) {
		return errors.New("--addr cannot be combined with --remote or --local")
	}
	return nil
}

// baseAddr is the address whose port --remote/--local keep: the remembered one
// when a preference exists, otherwise the default. Changing the exposure should
// not silently move the port.
func baseAddr() string {
	if a, ok := savedStartAddr(); ok {
		return a
	}
	return defaultAddr
}

// requestedAddr is the address a start request names, with no questions asked
// and no auth check: used where the address only needs to be reported (an
// already-running gateway compares ports with it).
func requestedAddr(req startRequest) string {
	if req.Addr != "" {
		return req.Addr
	}
	base := baseAddr()
	switch {
	case req.Remote:
		return anyBind(base)
	case req.Local:
		return helper.LoopbackBind(base)
	}
	return base
}

// resolveStartAddr decides where this start binds.
//
// Order: --addr (or --remote/--local) > remembered preference > loopback
// default. "Nothing given" and "given the default" must stay distinguishable —
// otherwise a remembered preference could never apply — which is why --addr is
// passed as an empty string when absent (runShell uses fs.Visit to tell them
// apart).
//
// A remote address (flag, remembered, or chosen at the prompt) is only honoured
// while auth is actually configured: credentials can be deleted after the fact,
// and silently exposing an unauthenticated plane is worse than a local-only
// start the operator can see and fix. --remote errors out rather than quietly
// falling back; a remembered preference and a prompt answer fall back with an
// explanation. (An explicit --addr still binds regardless — that is a promise
// the docs already make, and StartupAuth warns about it.)
func resolveStartAddr(req startRequest, ask askBind, auth bool) (string, error) {
	if req.Remote && !auth {
		return "", errRemoteNeedsAuth()
	}
	if req.Addr != "" || req.Remote || req.Local {
		return requestedAddr(req), nil
	}

	if addr, ok := savedStartAddr(); ok {
		if !helper.IsLoopbackBind(addr) && !auth {
			warnUnprotectedPreference(addr)
			return defaultAddr, nil
		}
		return addr, nil
	}

	if ask == nil {
		return defaultAddr, nil
	}
	choice, err := ask(defaultAddr, auth)
	if err != nil || choice.Addr == "" {
		return defaultAddr, nil
	}
	// Defense in depth: whatever the asker returned, a remote bind without
	// credentials is never taken. The prompt already explains this to the
	// operator; this keeps a custom or future non-interactive asker honest.
	if !helper.IsLoopbackBind(choice.Addr) && !auth {
		return defaultAddr, nil
	}
	if choice.Remember {
		if err := rememberStartAddr(choice.Addr); err != nil {
			fmt.Printf("%s %v\n", red("could not save the start preference:"), err)
		}
	}
	return choice.Addr, nil
}

// errRemoteNeedsAuth is the --remote/preference precondition failure: exposing
// the plane is only allowed once a credential exists.
func errRemoteNeedsAuth() error {
	return fmt.Errorf("--remote needs basic auth first: start with `niq --auth user:pass --remote`, or write credentials to %s", shortPath(store.AuthConfigPath()))
}

// warnUnprotectedPreference explains why a remembered remote bind was skipped
// instead of being applied silently.
func warnUnprotectedPreference(addr string) {
	fmt.Printf("%s %s\n", red("remote bind skipped:"), bold(addr)+dim(" is remembered but no auth is configured"))
	fmt.Printf("%s\n", dim("  starting on "+defaultAddr+" — set credentials with `niq --auth user:pass`, then start again"))
}

// anyBind turns addr into its wildcard sibling: same port, every interface. An
// empty host is the dual-stack wildcard, so IPv6-only peers can reach it too —
// the form the docs already use (`niq --addr :9527`).
func anyBind(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return net.JoinHostPort("", port)
}

// promptBind asks which interface to listen on, then whether to remember the
// answer. Picking "all interfaces" without credentials does not bind anything
// exposed: it explains the missing auth and starts on loopback for this run,
// leaving the question open for the next start.
//
// in is injected so the branch table is testable without a real terminal (the
// same seam ensure.go uses for the port-conflict prompt).
func promptBind(current string, auth bool, in *bufio.Reader) (bindChoice, error) {
	fmt.Printf("\n%s\n", bold("who may reach the gateway?"))
	fmt.Printf("  %s %s  %s\n", bold("[1]"), "this machine only", dim("— "+current+" (loopback)"))
	fmt.Printf("  %s %s\n", bold("[2]"), "all interfaces "+dim("— other machines can open the WebUI"))
	if !auth {
		fmt.Printf("      %s\n", dim("(needs basic auth first: niq --auth user:pass)"))
	}

	addr := current
	for {
		fmt.Print(dim("choice [1/2, Enter=1]: "))
		line, err := in.ReadString('\n')
		if err != nil {
			return bindChoice{Addr: current}, nil // EOF / Ctrl-D: keep the safe default
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "1", "local", "loopback":
			return bindChoice{Addr: addr, Remember: promptRemember(addr, in)}, nil
		case "2", "all", "remote", "any":
			if !auth {
				fmt.Println(red("a remote bind needs basic auth first:"))
				fmt.Println(dim("  niq --auth user:pass      (or write " + shortPath(store.AuthConfigPath()) + ")"))
				fmt.Println(dim("starting on loopback this time"))
				// Not remembered: the operator has not actually decided yet.
				return bindChoice{Addr: current}, nil
			}
			addr = anyBind(current)
			return bindChoice{Addr: addr, Remember: promptRemember(addr, in)}, nil
		}
	}
}

// promptRemember is the follow-up question. "no" is the deliberate "do not
// decide for me": the next start asks again.
func promptRemember(addr string, in *bufio.Reader) bool {
	fmt.Printf("%s %s\n", dim("remember this for future starts?"), dim("("+shortPath(store.ConfigPath())+")"))
	if addr != defaultAddr {
		fmt.Printf("%s\n", dim("  to change or clear it later, edit or delete that file"))
	}
	fmt.Print(dim("  [y/N]: "))
	line, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
