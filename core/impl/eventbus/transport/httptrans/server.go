package httptrans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"sync"
	"time"

	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// Server is the HTTP transport server — the "守塔人" for remote workers.
//
// It exposes two endpoints:
//   - GET /events?credential=<token> — SSE stream, creates BusSideChannel
//   - POST /publish — receive events from the worker
//
// Every request carries the same self-contained signed token as its credential.
// There is no session token beyond the BusSideChannel stored per connected
// worker, and the worker's identity is derived from the verified token on every
// request — never from a client self-declared worker_id.
//
// Usage:
//
//	srv := httptrans.NewServer(engine, registry, signer, ":8080")
//	srv.Start(ctx)
type Server struct {
	engine   *eventbus.Engine
	registry corebus.IdentityRegistry
	signer   *eventbus.TokenSigner // token mint/verify; nil refuses all remote auth
	addr     string
	listener net.Listener
	bound    string   // resolved host:port, empty until Bind
	sessions sync.Map // map[workerID]*busSide
}

// NewServer creates an HTTP transport server. signer authenticates remote
// connections (see eventbus.TokenSigner); a nil signer means no remote worker
// can connect (the bus is not exposing a token-issuing authority here).
func NewServer(engine *eventbus.Engine, registry corebus.IdentityRegistry, signer *eventbus.TokenSigner, addr string) *Server {
	return &Server{
		engine:   engine,
		registry: registry,
		signer:   signer,
		addr:     addr,
	}
}

// Bind binds the listen socket (eagerly, so a caller can learn the port before
// serving) and records the resolved host:port. With addr ":0" the OS assigns an
// ephemeral port read back via ResolvedAddr. Calling Bind twice returns the
// already-bound address. Safe to call before Start; Start binds if not yet.
func (s *Server) Bind() (string, error) {
	if s.listener != nil {
		return s.bound, nil
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return "", fmt.Errorf("httptrans: bind %s: %w", s.addr, err)
	}
	s.listener = ln
	s.bound = ln.Addr().String()
	return s.bound, nil
}

// ResolvedAddr returns the address actually bound (host:port). Empty until
// Bind has run; for a dynamic (":0") address this carries the assigned port.
func (s *Server) ResolvedAddr() string { return s.bound }

// Start binds (if needed) and serves HTTP. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	if s.listener == nil {
		if _, err := s.Bind(); err != nil {
			return err
		}
	}
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/publish", s.handlePublish)

	server := &stdhttp.Server{
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		server.Close()
	}()

	log.Printf("[httptrans] listening on %s", s.bound)
	if err := server.Serve(s.listener); err != stdhttp.ErrServerClosed {
		return err
	}
	return nil
}

// authenticate is the single auth boundary for the HTTP/SSE transport. It
// verifies the presented credential as a signed bus token and returns the
// derived worker identity, or an error that rejects the request with 401.
//
// The worker_id is taken from the token's verified claims — never from any
// client self-declared worker_id. authenticate also enforces remote
// connectability: an id whose registered Identity.Remote is false (an
// in-process managed worker) is rejected, closing the hole where a remote
// caller claims a managed worker's id. A credential-less request is rejected
// outright — there is no short-circuit for "no credential configured".
func (s *Server) authenticate(credential string) (corebus.Identity, error) {
	cl, err := s.signer.Verify(credential)
	if err != nil {
		return corebus.Identity{}, err
	}
	id, ok := s.registry.Lookup(cl.WorkerID)
	if !ok {
		return corebus.Identity{}, &authError{code: "unknown", msg: fmt.Sprintf("unknown worker: %s", cl.WorkerID)}
	}
	if !id.Remote {
		return corebus.Identity{}, &authError{code: "not_remote", msg: fmt.Sprintf("worker %s is not remote-connectable", cl.WorkerID)}
	}
	return id, nil
}

// authError carries a machine-readable code alongside the human message, so the
// transport can respond with a distinct indicator a remote peer can act on
// (most importantly "expired": the peer should re-provision a fresh token).
type authError struct {
	code string
	msg  string
}

func (e *authError) Error() string { return e.msg }

// authCode maps any authentication failure to a stable code string.
func (s *Server) authCode(err error) string {
	var ae *authError
	if errors.As(err, &ae) {
		return ae.code
	}
	switch {
	case errors.Is(err, eventbus.ErrTokenExpired):
		return "expired"
	case errors.Is(err, eventbus.ErrTokenRevoked):
		return "revoked"
	case errors.Is(err, eventbus.ErrNoSigner):
		return "unconfigured"
	default:
		return "invalid"
	}
}

// writeAuthError answers a failed authentication with HTTP 401 and a
// machine-readable JSON body plus a stable header, so a generic HTTP peer (not
// just our own Go client) can tell "expired → re-provision" from other
// failures. Status 401 is used for every auth failure (never 200), and there is
// intentionally no credential-less path.
func (s *Server) writeAuthError(w stdhttp.ResponseWriter, err error) {
	code := s.authCode(err)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Niq-Auth-Error", code)
	w.WriteHeader(stdhttp.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": err.Error()})
}

// ── Endpoints ──

// handleEvents opens an SSE stream for a worker and creates the BusSideChannel.
//
// The worker's identity is derived from the verified token presented in the
// credential query parameter, then the channel is attached to the engine and
// bound to that id for the rest of the connection.
//
// Query parameters:
//   - credential: the signed bus token (source of the authoritative worker id)
func (s *Server) handleEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	credential := r.URL.Query().Get("credential")

	id, err := s.authenticate(credential)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	workerID := id.WorkerID

	// Create BusSideChannel.
	toBus := make(chan corebus.Request, 64)
	toWorker := make(chan event.Event, 64)
	bs := &busSide{
		workerID: workerID,
		toWorker: toWorker,
		toBus:    toBus,
	}

	// Store for /publish handler.
	s.sessions.Store(workerID, bs)
	defer s.sessions.Delete(workerID)

	// Close the channel when this SSE stream ends (connection drop, restart,
	// shutdown) so the watch goroutine's Receive loop unblocks and the engine
	// detaches the worker. Without this, a dead network worker's stale channel
	// stays registered as "online" forever and blocks a reconnecting process.
	defer func() {
		if err := bs.Close(); err != nil {
			log.Printf("[httptrans] close %s: %v", workerID, err)
		}
	}()

	// Attach to engine — starts the watch goroutine.
	eventbus.Attach(r.Context(), s.engine, workerID, bs)

	// SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(stdhttp.Flusher)
	if !ok {
		stdhttp.Error(w, "streaming not supported", 500)
		return
	}

	// Flush the headers up front so the client's connect handshake (fetch to
	// /events) resolves immediately, independent of when the first event is
	// routed here. Without this, the response has no body data yet, the headers
	// are not flushed, and the worker's bus connection stays pending until some
	// other bus traffic wakes it up.
	flusher.Flush()

	log.Printf("[httptrans] SSE stream started for %s", workerID)

	// SSE loop: read from toWorker, push via SSE. A keepalive ticker writes
	// an SSE comment each interval so the connection never sits idle long
	// enough to trip a client/hop body timeout (e.g. undici's ~5min default on
	// the /events fetch body). Comments are silently ignored by SSE parsers.
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case evt, ok := <-toWorker:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()

		case <-r.Context().Done():
			log.Printf("[httptrans] SSE stream ended for %s", workerID)
			return
		}
	}
}

type publishRequest struct {
	WorkerID   string        `json:"worker_id"`
	Credential string        `json:"credential"`
	Type       string        `json:"type"` // "send" or "broadcast"
	Events     []event.Event `json:"events"`
	Targets    []string      `json:"targets,omitempty"`
	TraceID    string        `json:"trace_id,omitempty"`
}

// handlePublish receives a Request from a worker and forwards it to the engine.
func (s *Server) handlePublish(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.Method != "POST" {
		stdhttp.Error(w, "method not allowed", 405)
		return
	}

	var req publishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		stdhttp.Error(w, "invalid request", 400)
		return
	}

	// Authenticate by token and derive the worker id from its claims; the
	// body's self-declared req.WorkerID is never trusted for routing. A
	// connection is pinned to whatever id its token proved at /events, so
	// /publish cannot impersonate another worker under a different id.
	id, err := s.authenticate(req.Credential)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	// Find the session established at /events for that id.
	val, ok := s.sessions.Load(id.WorkerID)
	if !ok {
		stdhttp.Error(w, "worker not connected", 400)
		return
	}
	bs := val.(*busSide)

	// Build the Request.
	var rtype corebus.RequestType
	switch req.Type {
	case "send":
		rtype = corebus.RequestSend
	case "broadcast":
		rtype = corebus.RequestBroadcast
	default:
		stdhttp.Error(w, "invalid type", 400)
		return
	}

	busReq := corebus.Request{
		Type:    rtype,
		Events:  req.Events,
		Targets: req.Targets,
		TraceID: req.TraceID,
	}

	// Send to the bus side's toBus channel.
	select {
	case bs.toBus <- busReq:
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case <-r.Context().Done():
		stdhttp.Error(w, "request cancelled", 499)
	}
}
