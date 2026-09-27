package eventbus

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTokenSignerMintVerifyRoundTrip(t *testing.T) {
	s := NewTokenSigner([]byte("secret"))
	tok, err := s.Mint("w1", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	cl, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if cl.WorkerID != "w1" {
		t.Fatalf("worker id = %q, want w1", cl.WorkerID)
	}
	if cl.JTI == "" {
		t.Fatal("token carries no jti")
	}
}

func TestTokenSignerDistinctSecretsDoNotVerify(t *testing.T) {
	tok, _ := NewTokenSigner([]byte("a")).Mint("w1", time.Hour)
	if _, err := NewTokenSigner([]byte("b")).Verify(tok); err == nil {
		t.Fatal("token minted with a different secret must not verify")
	}
}

func TestTokenSignerRejectsTampered(t *testing.T) {
	s := NewTokenSigner([]byte("secret"))
	tok, _ := s.Mint("w1", time.Hour)

	// Flip one payload byte: the worker_id it claims changes, so the signature
	// must no longer match.
	parts := strings.Split(tok, ".")
	payloadRaw, _ := tokenUb64(parts[1])
	payloadRaw[0] ^= 0x01
	parts[1] = tokenB64(payloadRaw)
	if _, err := s.Verify(strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered token must not verify")
	}
}

func TestTokenSignerRejectsExpiredAndRevoked(t *testing.T) {
	s := NewTokenSigner([]byte("secret"))
	tok, _ := s.Mint("w1", -time.Second)
	if _, err := s.Verify(tok); err == nil {
		t.Fatal("expired token must not verify")
	}

	tok2, _ := s.Mint("w1", time.Hour)
	parts := strings.Split(tok2, ".")
	raw, _ := tokenUb64(parts[1])
	var cl TokenClaims
	if err := json.Unmarshal(raw, &cl); err != nil {
		t.Fatalf("decode: %v", err)
	}
	s.Revoke(cl.JTI)
	if _, err := s.Verify(tok2); err == nil {
		t.Fatal("revoked token must not verify")
	}
}

func TestTokenSignerVerifyRejectsEmptyCredential(t *testing.T) {
	s := NewTokenSigner([]byte("secret"))
	if _, err := s.Verify(""); err == nil {
		t.Fatal("empty credential must never verify")
	}
}

// TestTokenSignerNilRefuses documents that a nil signer refuses everything (no
// credential-less fallback on a bus with no signing authority).
func TestTokenSignerNilRefuses(t *testing.T) {
	var s *TokenSigner
	if _, err := s.Verify("anything"); err == nil {
		t.Fatal("nil signer must reject all credentials")
	}
}
