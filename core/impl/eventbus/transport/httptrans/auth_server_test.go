package httptrans

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// newAuthHarness builds a signer, a registry with two identities (a remotable
// worker and a managed in-process worker) and an httptrans server wired to
// them. It returns the signer for minting test tokens.
func newAuthHarness(t *testing.T) (*eventbus.TokenSigner, *Server) {
	t.Helper()
	reg, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "id.json"))
	if err != nil {
		t.Fatal(err)
	}
	signer := eventbus.NewTokenSigner([]byte("test-secret"))

	// A provisioned remote worker — may connect.
	tok, _ := signer.Mint("remote-w", time.Hour)
	if err := reg.Register(corebus.Identity{
		WorkerID: "remote-w", Type: "mcp", Remote: true, Credential: tok,
		PublishAllow: []event.PublishPattern{event.NewPublishPattern("*")},
	}); err != nil {
		t.Fatal(err)
	}
	// A managed in-process worker — its id must never be claimable remotely.
	if err := reg.Register(corebus.Identity{
		WorkerID: "reason", Type: "reason",
		PublishAllow:   []event.PublishPattern{event.NewPublishPattern("*")},
		SubscribeAllow: []event.EventPattern{{Type: "*"}},
	}); err != nil {
		t.Fatal(err)
	}

	eng := eventbus.NewEngine(reg, nil)
	return signer, NewServer(eng, reg, signer, ":0")
}

func TestAuthenticateDerivesIDFromToken(t *testing.T) {
	signer, srv := newAuthHarness(t)
	tok, _ := signer.Mint("remote-w", time.Hour)

	id, err := srv.authenticate(tok)
	if err != nil {
		t.Fatalf("authenticate valid remote token: %v", err)
	}
	if id.WorkerID != "remote-w" {
		t.Fatalf("authenticated id = %q, want remote-w (derived from token)", id.WorkerID)
	}
}

// TestAuthenticateRejectsManagedID closes the original hole: even a token whose
// claims name a managed worker's id is rejected because the identity is not
// remote-connectable.
func TestAuthenticateRejectsManagedID(t *testing.T) {
	signer, srv := newAuthHarness(t)
	tok, _ := signer.Mint("reason", time.Hour) // a token for the MANAGED id
	if _, err := srv.authenticate(tok); err == nil {
		t.Fatal("managed (non-remote) id must not authenticate over the transport")
	}
}

func TestAuthenticateRejectsNoCredentialAndForgery(t *testing.T) {
	_, srv := newAuthHarness(t)
	if _, err := srv.authenticate(""); err == nil {
		t.Fatal("empty credential must be rejected (no short-circuit)")
	}
	if _, err := srv.authenticate("niq1.abc.xyz"); err == nil {
		t.Fatal("forged credential must be rejected")
	}
}

// TestPublishPinnedToTokenID verifies /publish routes by the token-proven id,
// never the self-declared worker_id in the body.
func TestPublishPinnedToTokenID(t *testing.T) {
	signer, srv := newAuthHarness(t)
	tok, _ := signer.Mint("remote-w", time.Hour)

	// Seed a session only for the token's real id, as /events would.
	srv.sessions.Store("remote-w", &busSide{toBus: make(chan corebus.Request, 4)})

	// Body lies and claims to be "reason"; the token proves "remote-w", which
	// has a live session -> routed as remote-w (200).
	body := `{"worker_id":"reason","credential":"` + tok + `","type":"broadcast","events":[{"type":"x","payload":{}}]}`
	pr := httptest.NewRequest("POST", "/publish", strings.NewReader(body))
	pw := httptest.NewRecorder()
	srv.handlePublish(pw, pr)
	if pw.Code != 200 {
		t.Fatalf("publish status = %d, want 200 (token id has a live session)", pw.Code)
	}
}

// TestPublishRequiresSessionForTokenID verifies a token with no live /events
// session cannot publish, even if the body's self-declared id looked plausible.
func TestPublishRequiresSessionForTokenID(t *testing.T) {
	signer, srv := newAuthHarness(t)
	tok, _ := signer.Mint("remote-w", time.Hour)

	// No session at all for remote-w.
	bodyBytes := struct {
		WorkerID   string        `json:"worker_id"`
		Credential string        `json:"credential"`
		Type       string        `json:"type"`
		Events     []event.Event `json:"events"`
	}{WorkerID: "remote-w", Credential: tok, Type: "broadcast", Events: []event.Event{{Type: "x", Payload: map[string]any{}}}}
	b, _ := json.Marshal(bodyBytes)
	pr := httptest.NewRequest("POST", "/publish", strings.NewReader(string(b)))
	pw := httptest.NewRecorder()
	srv.handlePublish(pw, pr)
	if pw.Code != 401 && pw.Code != 400 {
		t.Fatalf("publish status = %d, want 401/400 for a token with no session", pw.Code)
	}
}

// TestAuthErrorDistinctExpiredCode verifies the transport answers an expired
// token with a machine-readable indicator (`{"error":"expired"}` + a stable
// header), and that the client side recognizes it as ExpiredToken.
func TestAuthErrorDistinctExpiredCode(t *testing.T) {
	signer, srv := newAuthHarness(t)
	tok, _ := signer.Mint("remote-w", -time.Hour) // already expired

	r := httptest.NewRequest("GET", "/events?credential="+tok, nil)
	w := httptest.NewRecorder()
	srv.handleEvents(w, r)
	if w.Code != 401 {
		t.Fatalf("status = %d, want 401 for an expired token", w.Code)
	}
	if got := w.Header().Get("X-Niq-Auth-Error"); got != "expired" {
		t.Fatalf("auth-error header = %q, want %q", got, "expired")
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if m["error"] != "expired" {
		t.Fatalf("body error code = %v, want expired", m["error"])
	}

	// The client side maps the body's expired code onto ErrRemoteTokenExpired.
	err := remoteAuthError(401, `{"error":"expired"}`)
	if !errors.Is(err, ErrRemoteTokenExpired) {
		t.Fatalf("remoteAuthError = %v, want it to wrap ErrRemoteTokenExpired", err)
	}
	// Other codes stay ordinary errors.
	if err := remoteAuthError(401, `{"error":"invalid"}`); errors.Is(err, ErrRemoteTokenExpired) {
		t.Fatal("non-expired code must not wrap ErrRemoteTokenExpired")
	}
}
