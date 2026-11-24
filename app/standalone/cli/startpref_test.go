package cli

import (
	"bufio"
	"errors"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/helper"
	"github.com/niq-run/niq/app/standalone/store"
)

// mustResolve fails the test on an unexpected resolution error.
func mustResolve(t *testing.T, req startRequest, ask askBind, auth bool) string {
	t.Helper()
	got, err := resolveStartAddr(req, ask, auth)
	if err != nil {
		t.Fatalf("resolveStartAddr(%+v): %v", req, err)
	}
	return got
}

// TestStartPrefRoundTrip covers the file the resolver reads: an absent file
// means "not decided", and a remembered address survives a write.
func TestStartPrefRoundTrip(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())

	if _, ok := savedStartAddr(); ok {
		t.Fatal("an empty root reported a remembered address")
	}
	if err := rememberStartAddr(":9527"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	got, ok := savedStartAddr()
	if !ok || got != ":9527" {
		t.Fatalf("saved = (%q,%v), want :9527", got, ok)
	}
}

// An explicit --addr is the operator overriding everything, including a prompt.
func TestResolveStartAddrExplicitWins(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if err := rememberStartAddr(":9527"); err != nil {
		t.Fatal(err)
	}
	asked := false
	ask := func(string, bool) (bindChoice, error) {
		asked = true
		return bindChoice{Addr: defaultAddr}, nil
	}
	if got := mustResolve(t, startRequest{Addr: "127.0.0.1:8080"}, ask, true); got != "127.0.0.1:8080" {
		t.Fatalf("explicit --addr = %q, want it to win", got)
	}
	if asked {
		t.Fatal("an explicit --addr must not prompt")
	}
}

// A remembered preference applies without asking — that is the whole point of
// remembering it.
func TestResolveStartAddrUsesRememberedWithoutAsking(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if err := rememberStartAddr(":9527"); err != nil {
		t.Fatal(err)
	}
	ask := func(string, bool) (bindChoice, error) {
		t.Fatal("a remembered preference must not prompt")
		return bindChoice{}, nil
	}
	if got := mustResolve(t, startRequest{}, ask, true); got != ":9527" {
		t.Fatalf("remembered = %q, want :9527", got)
	}
}

// No terminal and nothing remembered: the loopback default, no question.
func TestResolveStartAddrNoAskFallsBackToLoopback(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if got := mustResolve(t, startRequest{}, nil, true); got != defaultAddr {
		t.Fatalf("no ask = %q, want %q", got, defaultAddr)
	}
}

// Choosing "remember" persists the answer so the next start stops asking.
func TestResolveStartAddrRemembersChoice(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	ask := func(string, bool) (bindChoice, error) {
		return bindChoice{Addr: ":9527", Remember: true}, nil
	}
	if got := mustResolve(t, startRequest{}, ask, true); got != ":9527" {
		t.Fatalf("chosen = %q, want :9527", got)
	}
	if saved, ok := savedStartAddr(); !ok || saved != ":9527" {
		t.Fatalf("saved = (%q,%v), want the choice persisted", saved, ok)
	}
}

// Declining "remember" is the deliberate "do not decide for me": the answer
// applies to this start only, and the next one asks again.
func TestResolveStartAddrDoesNotRememberByDefault(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	calls := 0
	ask := func(string, bool) (bindChoice, error) {
		calls++
		return bindChoice{Addr: defaultAddr}, nil
	}
	if got := mustResolve(t, startRequest{}, ask, true); got != defaultAddr {
		t.Fatalf("chosen = %q, want %q", got, defaultAddr)
	}
	if _, ok := savedStartAddr(); ok {
		t.Fatal("a declined 'remember' must not be persisted")
	}
	mustResolve(t, startRequest{}, ask, true)
	if calls != 2 {
		t.Fatalf("ask calls = %d, want the question to come back next start", calls)
	}
}

// A remote bind without credentials is never taken, even if an asker returns
// one: the prompt explains it, and the resolver enforces it.
func TestResolveStartAddrRefusesRemoteChoiceWithoutAuth(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	ask := func(string, bool) (bindChoice, error) {
		return bindChoice{Addr: ":9527", Remember: true}, nil
	}
	if got := mustResolve(t, startRequest{}, ask, false); got != defaultAddr {
		t.Fatalf("remote without auth = %q, want the loopback default", got)
	}
	if _, ok := savedStartAddr(); ok {
		t.Fatal("an unprotected remote choice must not be remembered")
	}
}

// The same rule for a preference saved earlier and then stripped of its auth:
// credentials can be deleted after the fact, and silently exposing the plane is
// worse than a local-only start the operator can see.
func TestResolveStartAddrSkipsRememberedRemoteWithoutAuth(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if err := rememberStartAddr(":9527"); err != nil {
		t.Fatal(err)
	}
	if got := mustResolve(t, startRequest{}, nil, false); got != defaultAddr {
		t.Fatalf("remembered remote without auth = %q, want the loopback default", got)
	}
}

// A remembered loopback bind is a real preference too: it is honoured without
// asking, and it needs no auth.
func TestResolveStartAddrRememberedLoopback(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if err := rememberStartAddr(defaultAddr); err != nil {
		t.Fatal(err)
	}
	ask := func(string, bool) (bindChoice, error) {
		t.Fatal("a remembered loopback preference must not prompt")
		return bindChoice{}, nil
	}
	if got := mustResolve(t, startRequest{}, ask, false); got != defaultAddr {
		t.Fatalf("remembered loopback = %q, want %q", got, defaultAddr)
	}
}

// A failing prompt degrades to the safe default instead of blocking a start.
func TestResolveStartAddrAskErrorFallsBack(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	ask := func(string, bool) (bindChoice, error) {
		return bindChoice{}, errors.New("boom")
	}
	if got := mustResolve(t, startRequest{}, ask, true); got != defaultAddr {
		t.Fatalf("ask error = %q, want %q", got, defaultAddr)
	}
}

// --remote binds every interface without asking, keeping the default port.
func TestResolveStartAddrRemoteFlag(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if got := mustResolve(t, startRequest{Remote: true}, nil, true); got != anyBind(defaultAddr) {
		t.Fatalf("--remote = %q, want %q", got, anyBind(defaultAddr))
	}
	// Nothing was decided for the next start: the flag speaks for this run.
	if _, ok := savedStartAddr(); ok {
		t.Fatal("--remote must not persist a preference")
	}
}

// --remote without credentials is refused outright: it is the convenient path,
// so it must not be able to expose an unauthenticated plane. (--addr remains
// the explicit escape hatch, and StartupAuth warns about it.)
func TestResolveStartAddrRemoteFlagNeedsAuth(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if _, err := resolveStartAddr(startRequest{Remote: true}, nil, false); err == nil {
		t.Fatal("--remote without auth must fail")
	} else if !strings.Contains(err.Error(), "--auth") {
		t.Fatalf("err = %q, want it to name the fix", err)
	}
}

// --remote keeps the remembered port, so a saved ":9000" does not silently move.
func TestResolveStartAddrRemoteFlagKeepsSavedPort(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if err := rememberStartAddr("127.0.0.1:9000"); err != nil {
		t.Fatal(err)
	}
	if got := mustResolve(t, startRequest{Remote: true}, nil, true); got != ":9000" {
		t.Fatalf("--remote = %q, want :9000", got)
	}
}

// --local is the one-off opposite: loopback now even though the preference says
// otherwise, keeping the remembered port.
func TestResolveStartAddrLocalFlagOverridesPreference(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if err := rememberStartAddr(":9000"); err != nil {
		t.Fatal(err)
	}
	if got := mustResolve(t, startRequest{Local: true}, nil, true); got != "127.0.0.1:9000" {
		t.Fatalf("--local = %q, want 127.0.0.1:9000", got)
	}
	// The preference itself is untouched.
	if saved, ok := savedStartAddr(); !ok || saved != ":9000" {
		t.Fatalf("saved = (%q,%v), want the preference untouched", saved, ok)
	}
}

// Contradictory combinations are rejected before anything is resolved.
func TestStartRequestValidate(t *testing.T) {
	cases := []struct {
		req  startRequest
		want bool // true = valid
	}{
		{startRequest{}, true},
		{startRequest{Addr: ":9527"}, true},
		{startRequest{Remote: true}, true},
		{startRequest{Local: true}, true},
		{startRequest{Remote: true, Local: true}, false},
		{startRequest{Addr: ":9527", Remote: true}, false},
		{startRequest{Addr: ":9527", Local: true}, false},
	}
	for _, tc := range cases {
		if err := tc.req.validate(); (err == nil) != tc.want {
			t.Fatalf("%+v validate = %v, want valid=%v", tc.req, err, tc.want)
		}
	}
}

// anyBind must keep the port and widen only the host.
func TestAnyBindKeepsPort(t *testing.T) {
	if got := anyBind(defaultAddr); got != ":9527" {
		t.Fatalf("anyBind(%q) = %q, want :9527", defaultAddr, got)
	}
	if got := anyBind("127.0.0.1:9000"); got != ":9000" {
		t.Fatalf("anyBind = %q, want :9000", got)
	}
}

// TestPromptBind is the branch table for the interactive choice: what each
// answer binds, and whether it is remembered. The auth-missing case is the
// important one — choosing "all interfaces" there must not expose anything.
func TestPromptBind(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		auth     bool
		wantAddr string
		remember bool
	}{
		{"enter keeps loopback", "\n", false, defaultAddr, false},
		{"1 keeps loopback", "1\nn\n", false, defaultAddr, false},
		{"1 remembered", "1\ny\n", false, defaultAddr, true},
		{"2 with auth binds all", "2\nn\n", true, anyBind(defaultAddr), false},
		{"2 with auth remembered", "2\ny\n", true, anyBind(defaultAddr), true},
		// Auth is required for a remote bind: the choice degrades to loopback
		// and is not remembered, so the question comes back next start.
		{"2 without auth stays loopback", "2\n", false, defaultAddr, false},
		// Unknown words are ignored, then EOF keeps the safe default.
		{"garbage then eof", "nonsense\n", true, defaultAddr, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(store.Env, t.TempDir())
			got, err := promptBind(defaultAddr, tc.auth, bufio.NewReader(strings.NewReader(tc.input)))
			if err != nil {
				t.Fatalf("promptBind: %v", err)
			}
			if got.Addr != tc.wantAddr || got.Remember != tc.remember {
				t.Fatalf("choice = (%q,%v), want (%q,%v)", got.Addr, got.Remember, tc.wantAddr, tc.remember)
			}
		})
	}
}

// requestedAddr is the projection used when the answer only needs reporting:
// it must never ask, and must never fail.
func TestRequestedAddr(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())
	if got := requestedAddr(startRequest{}); got != defaultAddr {
		t.Fatalf("empty request = %q, want %q", got, defaultAddr)
	}
	if got := requestedAddr(startRequest{Remote: true}); got != anyBind(defaultAddr) {
		t.Fatalf("remote = %q, want %q", got, anyBind(defaultAddr))
	}
	if got := requestedAddr(startRequest{Local: true}); got != helper.LoopbackBind(defaultAddr) {
		t.Fatalf("local = %q, want %q", got, helper.LoopbackBind(defaultAddr))
	}
}
