package httptrans

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"sync"
	"time"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/eventbus"
)

// Server is the HTTP transport server — the "守塔人" for remote workers.
//
// It exposes two endpoints:
//   - GET /events?worker_id=xxx&credential=yyy — SSE stream, creates BusSideChannel
//   - POST /publish — receive events from the worker
//
// Every request carries its own credentials. There is no session state
// beyond the BusSideChannel stored for each connected worker.
//
// Usage:
//
//	srv := httptrans.NewServer(engine, registry, ":8080")
//	srv.Start(ctx)
type Server struct {
	engine   *eventbus.Engine
	registry corebus.IdentityRegistry
	addr     string
	listener net.Listener
	bound    string   // resolved host:port, empty until Bind
	sessions sync.Map // map[workerID]*busSide
}

// NewServer creates an HTTP transport server.
func NewServer(engine *eventbus.Engine, registry corebus.IdentityRegistry, addr string) *Server {
	return &Server{
		engine:   engine,
		registry: registry,
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

// validateCredential checks that the worker exists and the credential matches.
func (s *Server) validateCredential(workerID, credential string) error {
	id, ok := s.registry.Lookup(workerID)
	if !ok {
		return fmt.Errorf("unknown worker: %s", workerID)
	}
	if id.Credential != "" && id.Credential != credential {
		return fmt.Errorf("invalid credential")
	}
	return nil
}

// ── Endpoints ──

// handleEvents opens an SSE stream for a worker and creates the BusSideChannel.
//
// This is where the "线头插到球上" happens — the worker opens an SSE
// connection, the server validates its credentials, creates a BusSideChannel,
// attaches it to the engine, and starts pushing events.
//
// Query parameters:
//   - worker_id:  the worker's identity
//   - credential: the worker's credential for authentication
func (s *Server) handleEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	workerID := r.URL.Query().Get("worker_id")
	credential := r.URL.Query().Get("credential")

	if workerID == "" {
		stdhttp.Error(w, "worker_id required", 400)
		return
	}
	if err := s.validateCredential(workerID, credential); err != nil {
		stdhttp.Error(w, err.Error(), 401)
		return
	}

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
	if err := s.validateCredential(req.WorkerID, req.Credential); err != nil {
		stdhttp.Error(w, err.Error(), 401)
		return
	}

	// Find the session.
	val, ok := s.sessions.Load(req.WorkerID)
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
