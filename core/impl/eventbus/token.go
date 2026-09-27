package eventbus

// Bus-level token authentication for remote transport connections.
//
// Every non-in-process connection to the bus (today HTTP/SSE via httptrans,
// tomorrow WebSocket / TCP / any other remote transport) presents a
// SELF-CONTAINED signed token as its credential. The token is minted by the
// bus via a single root-secret HMAC-SHA256 signature over
// {worker_id, iat, exp, jti} and verified statelessly at each transport's
// connect/publish boundary.
//
// Because the worker_id lives inside the signed token, every transport derives
// the caller's identity from the token's verified claims rather than from any
// worker_id the client self-declares — an attacker cannot present a worker_id
// the bus never minted a token for. The TokenSigner therefore lives here in the
// bus core, shared by every remote transport, not in any one transport
// implementation.
//
//   - identity.Remote gates that an id may connect via a remote transport;
//     managed in-process workers keep Remote=false and are never minted a
//     token, so their ids can never appear over the wire.
//   - temp (launched-third-party) identities live in an in-memory registry tier
//     that dies with the process; revocations are likewise held in memory unless
//     a transport configures persistence.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

const tokenVersion = "niq1"

// Token verification outcomes as typed sentinel errors, so a transport can
// tell an expired/revoked credential apart from a malformed one and surface a
// distinct indicator to the peer (e.g. "re-provision" vs "fix your request").
var (
	ErrNoSigner     = errors.New("no token signer configured")
	ErrInvalidToken = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
	ErrTokenRevoked = errors.New("token revoked")
)

// tokenRevokeGrace is how long a revoked jti is remembered so a token revoked
// just before expiry is still rejected by stragglers, without keeping entries
// forever.
const tokenRevokeGrace = 2 * time.Minute

// TokenClaims is the authenticated payload carried inside a signed token.
// These are exactly the fields the bus controls at mint time — there is no
// client input.
type TokenClaims struct {
	WorkerID string `json:"worker_id"`
	IAT      int64  `json:"iat"`
	EXP      int64  `json:"exp"`
	JTI      string `json:"jti"`
}

// TokenSigner mints and verifies HMAC-SHA256 signed tokens anchored on a secret
// only the bus holds. Verification is stateless (signature + exp + revocation
// are all self-checked except the revocation set, held by jti).
type TokenSigner struct {
	secret []byte

	mu      sync.Mutex
	revoked map[string]time.Time // jti → expiry of the revocation record
}

// NewTokenSigner creates a token signer from the bus's root secret.
func NewTokenSigner(secret []byte) *TokenSigner {
	return &TokenSigner{secret: secret, revoked: make(map[string]time.Time)}
}

func tokenB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func tokenUb64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// Mint issues a signed token for workerID valid for ttl. Each token carries a
// fresh jti so it can be individually revoked.
func (s *TokenSigner) Mint(workerID string, ttl time.Duration) (string, error) {
	jtiBytes := make([]byte, 12)
	if _, err := rand.Read(jtiBytes); err != nil {
		return "", err
	}
	now := time.Now()
	cl := TokenClaims{
		WorkerID: workerID,
		IAT:      now.Unix(),
		EXP:      now.Add(ttl).Unix(),
		JTI:      hex.EncodeToString(jtiBytes),
	}
	payload, err := json.Marshal(cl)
	if err != nil {
		return "", err
	}
	body := tokenVersion + "." + tokenB64(payload)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	return body + "." + tokenB64(mac.Sum(nil)), nil
}

// Verify authenticates a token: signature, expiry and revocation, returning the
// authenticated claims (carrying the authoritative worker_id) on success. A
// nil-signer bus rejects everything — there is no credential-less fallback.
func (s *TokenSigner) Verify(token string) (TokenClaims, error) {
	if s == nil {
		return TokenClaims{}, ErrNoSigner
	}
	if token == "" {
		return TokenClaims{}, ErrInvalidToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenVersion {
		return TokenClaims{}, ErrInvalidToken
	}
	body := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	sig, err := tokenUb64(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return TokenClaims{}, ErrInvalidToken
	}
	raw, err := tokenUb64(parts[1])
	if err != nil {
		return TokenClaims{}, ErrInvalidToken
	}
	var cl TokenClaims
	if err := json.Unmarshal(raw, &cl); err != nil {
		return TokenClaims{}, ErrInvalidToken
	}
	if cl.WorkerID == "" || cl.JTI == "" {
		return TokenClaims{}, ErrInvalidToken
	}
	if cl.Expired(time.Now()) {
		return TokenClaims{}, ErrTokenExpired
	}
	if s.isRevoked(cl.JTI, time.Now()) {
		return TokenClaims{}, ErrTokenRevoked
	}
	return cl, nil
}

// Expired reports whether the token is past its expiry (exp 0 = never expires).
func (c TokenClaims) Expired(now time.Time) bool {
	return c.EXP > 0 && now.Unix() >= c.EXP
}

// Revoke records jti so any still-fresh token carrying it is rejected.
func (s *TokenSigner) Revoke(jti string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[jti] = time.Now().Add(tokenRevokeGrace)
}

func (s *TokenSigner) isRevoked(jti string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for j, exp := range s.revoked {
		if now.After(exp) {
			delete(s.revoked, j)
		}
	}
	_, ok := s.revoked[jti]
	return ok
}
