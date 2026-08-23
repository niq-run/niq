// Package webui provides a web-based human interface for niq.
//
// It serves a React SPA that communicates with the backend via HTTP and SSE.
// It is owned by the swarm binary, not by the HIW worker — the HTTP server
// directly references HIW (for sending input) and EventLog (for streaming).
package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/pkg/service/eventbus"
	eventbusapi "github.com/54c1/niq/pkg/service/eventbus/api"
	"github.com/54c1/niq/pkg/service/workerhost"
	"github.com/54c1/niq/pkg/worker/hiw"
)

//go:embed assets/dist/*
var embeddedAssets embed.FS

// ContextInfo tells the single SPA which mode it is in: control (no project
// attached — only project management is available) or project (a specific
// project is attached — talk/events run against it).
type ContextInfo struct {
	Mode       string `json:"mode"`                // "control" | "project"
	Project    string `json:"project,omitempty"`  // project id in project mode
	ControlURL string `json:"control_url,omitempty"` // control-plane base URL (for project→control jumps)
}

// ArchivedStore reads/writes a project's archived-worker set (persisted in the
// project.json worker definitions). nil disables the feature (endpoints reply
// with an empty archived set).
type ArchivedStore interface {
	Archived() []string
	SetArchived(id string, v bool) error
}

// AssetsFS exposes the embedded SPA static assets for reuse by the control server.
func AssetsFS() (fs.FS, error) {
	return fs.Sub(embeddedAssets, "assets/dist")
}

// Server is the WebUI HTTP server.
type Server struct {
	hiw       *hiw.Worker
	server    *http.Server
	listener  net.Listener
	addr      string // resolved listen address (host:port), empty until Bind
	devMode   bool   // when true, static assets are proxied to Vite dev server
	eventLog  *eventbusapi.EventLog
	engine    *eventbus.Engine
	registry  corebus.IdentityRegistry
	workerSvc *workerhost.WorkerService

	ctxMu    sync.RWMutex
	context  ContextInfo
	archived ArchivedStore
}

// New creates a WebUI Server.
// devMode enables Vite-proxy mode (frontend runs on :5173, APIs stay on addr).
func New(h *hiw.Worker, el *eventbusapi.EventLog, engine *eventbus.Engine, workerSvc *workerhost.WorkerService, registry corebus.IdentityRegistry, addr string, devMode bool) *Server {
	s := &Server{hiw: h, eventLog: el, engine: engine, workerSvc: workerSvc, registry: registry, devMode: devMode}
	mux := http.NewServeMux()

	// ── API routes ──

	// SSE: real-time event stream (from EventLog, which gets all events via Engine.OnEvent).
	mux.HandleFunc("GET /api/stream", s.serveSSE)

	// Input: publish user input to the bus as HIW.
	mux.HandleFunc("POST /api/input", s.handleInput)

	// Workers: list all registered workers with identity, connection and
	// host-managed lifecycle state.
	mux.HandleFunc("GET /api/workers", s.handleWorkers)

	// Suspend / resume a host-managed worker (via the host worker's tools).
	mux.HandleFunc("POST /api/workers/{id}/suspend", s.handleSuspend)
	mux.HandleFunc("POST /api/workers/{id}/resume", s.handleResume)

	// Events pagination: load events before a given anchor.
	mux.HandleFunc("GET /api/events/before/{id}", s.handleLoadBefore)

	// Abort: interrupt a worker's current reasoning.
	mux.HandleFunc("POST /api/abort", s.handleAbort)

	// ── Static assets ──
	if devMode {
		log.Println("[webui] dev mode: static assets served by Vite on :5173")
	} else {
		sub, err := AssetsFS()
		if err != nil {
			log.Fatalf("[webui] embed fs: %v", err)
		}
		mux.Handle("GET /", http.FileServer(http.FS(sub)))
	}

	// Mode context: tells the single SPA whether this WebUI is a control plane
	// or a project instance, and where the control plane lives.
	mux.HandleFunc("GET /api/context", s.handleContext)

	// Archived workers: which workers are hidden from the selector (by default),
	// and toggling that flag (persisted in the project.json worker definitions).
	mux.HandleFunc("GET /api/archived", s.handleGetArchived)
	mux.HandleFunc("POST /api/workers/{id}/archived", s.handleSetArchived)

	s.server = &http.Server{Addr: addr, Handler: cors(mux)}
	return s
}

// Bind binds the listen socket (eagerly, so a caller can learn the port before
// serving) and records the resolved host:port. With addr ":0" the OS assigns
// an ephemeral port read back via ResolvedAddr. Calling Bind twice returns the
// already-bound address. Safe to call before Start; Start binds if not yet.
func (s *Server) Bind() (string, error) {
	if s.listener != nil {
		return s.addr, nil
	}
	ln, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return "", fmt.Errorf("webui: bind %s: %w", s.server.Addr, err)
	}
	s.listener = ln
	s.addr = ln.Addr().String()
	s.server.Addr = s.addr
	return s.addr, nil
}

// ResolvedAddr returns the address actually bound (host:port). Empty until
// Bind has run; for a dynamic (":0") address this carries the assigned port.
func (s *Server) ResolvedAddr() string { return s.addr }

// Start binds (if needed) and serves HTTP. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	if s.listener == nil {
		if _, err := s.Bind(); err != nil {
			return err
		}
	}
	log.Printf("[webui] listening on %s", s.addr)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.server.Shutdown(shutdownCtx)
	}()

	if err := s.server.Serve(s.listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("webui: %w", err)
	}
	return nil
}

// SetArchivedStore attaches the project's archived-worker store (nil to disable).
func (s *Server) SetArchivedStore(as ArchivedStore) {
	s.archived = as
}

// SetContext records the mode context the single SPA should render in. Safe to
// call from the swarm assembly after construction and before Start.
func (s *Server) SetContext(ctx ContextInfo) {
	s.ctxMu.Lock()
	defer s.ctxMu.Unlock()
	s.context = ctx
}

// handleGetArchived returns the archived-worker ids (empty store → empty list).
func (s *Server) handleGetArchived(w http.ResponseWriter, r *http.Request) {
	if s.archived == nil {
		json.NewEncoder(w).Encode([]string{})
		return
	}
	json.NewEncoder(w).Encode(s.archived.Archived())
}

// handleSetArchived toggles whether a worker is archived, persisting the change.
func (s *Server) handleSetArchived(w http.ResponseWriter, r *http.Request) {
	if s.archived == nil {
		http.Error(w, "archive store unavailable", 404)
		return
	}
	id := r.PathValue("id")
	var body struct {
		Archived bool `json:"archived"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if err := s.archived.SetArchived(id, body.Archived); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"archived": s.archived.Archived()})
}

// handleContext reports the SPA mode context.
func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	s.ctxMu.RLock()
	defer s.ctxMu.RUnlock()
	json.NewEncoder(w).Encode(s.context)
}

// ── SSE ──

func (s *Server) serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	filter := eventbusapi.Filter{
		WorkerIDs: r.URL.Query()["worker"],
		TraceID:   r.URL.Query().Get("trace"),
		Type:      event.EventType(r.URL.Query().Get("type")),
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, err := s.eventLog.Follow(r.Context(), filter, limit)
	if err != nil {
		log.Printf("[webui] follow error: %v", err)
		return
	}

	for evt := range ch {
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
}

// ── API handlers ──

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text      string `json:"text"`
		Target    string `json:"target,omitempty"`
		InputMode string `json:"input_mode,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.hiw.SendInput(r.Context(), body.Text, body.Target, body.InputMode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// WorkerView is the unified view of a worker: its registered identity (from
// the bus registry), its connection status, and — if host-managed — its
// lifecycle state.
type WorkerView struct {
	ID             string               `json:"id"`
	Type           string               `json:"type"`
	Credential     string               `json:"credential,omitempty"`
	PublishAllow   []string             `json:"publish_allow,omitempty"`
	SubscribeAllow []event.EventPattern `json:"subscribe_allow,omitempty"`
	Online         bool                 `json:"online"`
	Managed        bool                 `json:"managed"`
	State          string               `json:"state,omitempty"` // "running" | "suspended" (managed only)
}

// handleWorkers returns every registered worker identity merged with its
// connection status and host-managed lifecycle state.
func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	online := map[string]bool{}
	for _, id := range s.engine.OnlineWorkers() {
		online[id] = true
	}

	managed := map[string]string{} // id → state
	for _, wi := range s.workerSvc.ListWorkers("") {
		managed[wi.ID] = string(wi.State)
	}

	var views []WorkerView
	for _, id := range s.registry.List() {
		v := WorkerView{ID: id.WorkerID, Type: id.Type, Credential: id.Credential,
			PublishAllow: id.PublishAllow, SubscribeAllow: id.SubscribeAllow,
			Online: online[id.WorkerID]}
		if state, ok := managed[id.WorkerID]; ok {
			v.Managed = true
			v.State = state
		}
		views = append(views, v)
	}

	// Include managed workers whose identity is not yet registered (transient).
	for _, wi := range s.workerSvc.ListWorkers("") {
		if _, ok := s.registry.Lookup(wi.ID); !ok {
			views = append(views, WorkerView{
				ID: wi.ID, Type: wi.Type, Managed: true, State: string(wi.State), Online: online[wi.ID],
			})
		}
	}

	// The registry's List is already ID-sorted; sort the merged view too, so
	// transient managed workers land at a deterministic position instead of
	// trailing in an ever-changing insertion order.
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })

	json.NewEncoder(w).Encode(views)
}

// handleSuspend suspends a host-managed worker by publishing tool.requested to
// the host worker (data-plane, auditable).
func (s *Server) handleSuspend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	evts := []event.Event{event.New(event.TypeToolRequested, "hiw", map[string]any{
		"call_id":   "webui-suspend-" + id,
		"name":      "suspend",
		"arguments": map[string]any{"worker_id": id},
	})}
	_ = s.hiw.Channel.Send(r.Context(), evts[0], "host")
	w.WriteHeader(http.StatusAccepted)
}

// handleResume resumes a host-managed worker via the host worker's tools.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	evt := event.New(event.TypeToolRequested, "hiw", map[string]any{
		"call_id":   "webui-resume-" + id,
		"name":      "resume",
		"arguments": map[string]any{"worker_id": id},
	})
	_ = s.hiw.Channel.Send(r.Context(), evt, "host")
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	evt := event.New(event.TypeWorkerAbort, "hiw", map[string]any{
		"worker_id": "hiw",
	})
	if body.Target != "" {
		_ = s.hiw.Channel.Send(r.Context(), evt, body.Target)
	} else {
		_ = s.hiw.Channel.Broadcast(r.Context(), evt)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleLoadBefore(w http.ResponseWriter, r *http.Request) {
	anchor := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	filter := eventbusapi.Filter{
		WorkerIDs: r.URL.Query()["worker"],
		TraceID:   r.URL.Query().Get("trace"),
		Type:      event.EventType(r.URL.Query().Get("type")),
	}

	events, err := s.eventLog.LoadBefore(r.Context(), filter, anchor, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(events)
}

// ── CORS ──

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
