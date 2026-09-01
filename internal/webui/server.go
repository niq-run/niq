// Package webui provides a web-based human interface for niq.
//
// It serves a React SPA that communicates with the backend via HTTP and SSE.
// It is owned by the project/control binaries, not by the HIW worker — the
// HTTP server directly references HIW (for sending input) and EventLog (for
// streaming).
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

	"github.com/google/uuid"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	reasonBase "github.com/niq-run/niq/pkg/reason"
	"github.com/niq-run/niq/pkg/eventbus"
	eventbusapi "github.com/niq-run/niq/pkg/eventbus/api"
	"github.com/niq-run/niq/pkg/services/workerhost"
	"github.com/niq-run/niq/pkg/workers/hiw"
)

//go:embed assets/dist/*
var embeddedAssets embed.FS

// ContextInfo tells the single SPA which mode it is in: control (no project
// attached — only project management is available) or project (a specific
// project is attached — talk/events run against it).
type ContextInfo struct {
	Mode       string `json:"mode"`                  // "control" | "project"
	Project    string `json:"project,omitempty"`     // project id in project mode
	ControlURL string `json:"control_url,omitempty"` // control-plane base URL (for project→control jumps)
}

// ArchivedStore reads/writes a project's archived-worker set (persisted in the
// project.json worker definitions). nil disables the feature (endpoints reply
// with an empty archived set).
type ArchivedStore interface {
	Archived() []string
	SetArchived(id string, v bool) error
}

// UnmanagedStatus is a read-only view of an external (unmanaged) worker.
type UnmanagedStatus struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	State string `json:"state"` // "running" | "stopped"
	Alive bool   `json:"alive"`
}

// UnmanagedController controls external (unmanaged) workers. Implemented by
// the project assembly layer; nil disables the endpoints.
type UnmanagedController interface {
	Start(id string) error
	Stop(id string) error
	Restart(id string) error
	List() []UnmanagedStatus
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
	unmngd   UnmanagedController
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

	// Model provider: read and switch a reason worker's active LLM provider.
	// These go over the bus as worker.query / worker.update and wait for the
	// worker's worker.status / worker.updated reply.
	mux.HandleFunc("GET /api/workers/{id}/providers", s.handleWorkerProviders)
	mux.HandleFunc("POST /api/workers/{id}/provider", s.handleWorkerSetProvider)

	// Start / stop / restart an external (unmanaged) worker.
	mux.HandleFunc("POST /api/workers/{id}/start", s.handleUnmanagedStart)
	mux.HandleFunc("POST /api/workers/{id}/stop", s.handleUnmanagedStop)
	mux.HandleFunc("POST /api/workers/{id}/restart", s.handleUnmanagedRestart)

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

// SetUnmanagedController attaches the external-worker controller (nil disables
// the start/stop/restart endpoints).
func (s *Server) SetUnmanagedController(c UnmanagedController) {
	s.unmngd = c
}

// SetContext records the mode context the single SPA should render in. Safe to
// call from the project assembly after construction and before Start.
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

// handleSetArchived toggles whether a worker is archived. Archiving suspends the
// worker first (archive == suspend + mark); restoring resumes it and unmarks it.
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

	// Is this worker managed by this project's host? Managed workers are the ones
	// archive may suspend/resume; unmanaged ones are hidden only.
	managed := false
	if s.workerSvc != nil {
		for _, wi := range s.workerSvc.ListWorkers("") {
			if wi.ID == id {
				managed = true
				break
			}
		}
	}
	if body.Archived {
		if managed {
			// Archive == suspend first, then mark.
			if err := s.workerSvc.SuspendWorker(id); err != nil {
				http.Error(w, "suspend before archive: "+err.Error(), 500)
				return
			}
		}
	} else if managed {
		_ = s.workerSvc.ResumeWorker(r.Context(), id)
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// The stream now carries only events newer than `watermark`; history is
	// paged in separately by the client. Advertise the watermark up front as a
	// control event so the client can start its backwards pagination.
	ch, watermark, err := s.eventLog.FollowLive(r.Context(), filter)
	if err != nil {
		log.Printf("[webui] follow error: %v", err)
		return
	}

	fmt.Fprintf(w, "event: watermark\ndata: %s\n\n", watermark)
	flusher.Flush()

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
// lifecycle state, or if external — its supervision state.
type WorkerView struct {
	ID             string               `json:"id"`
	Type           string               `json:"type"`
	Credential     string               `json:"credential,omitempty"`
	PublishAllow   []string             `json:"publish_allow,omitempty"`
	SubscribeAllow []event.EventPattern `json:"subscribe_allow,omitempty"`
	Online         bool                 `json:"online"`
	Managed        bool                 `json:"managed"`
	State          string               `json:"state,omitempty"` // "running" | "suspended" (managed only)
	Unmanaged      bool                 `json:"unmanaged,omitempty"`
	UnmanagedState string               `json:"unmanaged_state,omitempty"` // "running" | "stopped"
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

	unmngd := map[string]UnmanagedStatus{}
	if s.unmngd != nil {
		for _, st := range s.unmngd.List() {
			unmngd[st.ID] = st
		}
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
		if st, ok := unmngd[id.WorkerID]; ok {
			v.Unmanaged = true
			v.UnmanagedState = st.State
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

// handleSuspend suspends a host-managed worker by sending the host worker's
// suspend event (data-plane, auditable).
func (s *Server) handleSuspend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	evt := event.New("suspend", "webui-hiw", map[string]any{
		"arguments": map[string]any{"worker_id": id},
	})
	evt.RequestId = "webui-suspend-" + id
	_ = s.hiw.Channel.Send(r.Context(), evt, "host")
	w.WriteHeader(http.StatusAccepted)
}

// handleResume resumes a host-managed worker via the host worker's resume
// event.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	evt := event.New("resume", "webui-hiw", map[string]any{
		"arguments": map[string]any{"worker_id": id},
	})
	evt.RequestId = "webui-resume-" + id
	_ = s.hiw.Channel.Send(r.Context(), evt, "host")
	w.WriteHeader(http.StatusAccepted)
}

// askTimeout bounds how long we wait for a worker to answer a meta query. The
// provider.list round is the slow one: it queries every configured provider's
// model-list endpoint, so it needs several seconds rather than a few hundred
// milliseconds.
const askTimeout = 20 * time.Second

// ask publishes evt to one worker and waits for its reply, correlating the two
// by trace id. The subscription is registered BEFORE publishing so a fast reply
// cannot be missed; the worker copies the request's trace id onto its reply,
// which is what makes the match unambiguous.
//
// Send never reports delivery failure — it only enqueues, and the engine drops
// events addressed to a worker that is not connected — so callers must
// pre-check that the worker is online (see workerOnline) or they would sit here
// until the timeout for a worker that will never answer.
func (s *Server) ask(ctx context.Context, target string, evt event.Event, want ...event.EventType) (event.Event, error) {
	traceID, _ := uuid.NewV7() // only fails if crypto/rand fails
	tid := traceID.String()
	evt.TraceID = tid // event.New leaves TraceID empty; we own correlation here

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := s.eventLog.Subscribe(subCtx, eventbusapi.Filter{TraceID: tid})
	if err != nil {
		return event.Event{}, fmt.Errorf("subscribe for reply: %w", err)
	}

	if err := s.hiw.Channel.Send(ctx, evt, target); err != nil {
		return event.Event{}, fmt.Errorf("send %s to %s: %w", evt.Type, target, err)
	}

	timer := time.NewTimer(askTimeout)
	defer timer.Stop()
	for {
		select {
		case got, open := <-ch:
			// Subscribe closes the channel when its context is done; without
			// this check a closed channel would yield zero-valued events
			// forever and spin until the timeout.
			if !open {
				return event.Event{}, fmt.Errorf("event stream closed waiting for %v from %s", want, target)
			}
			for _, w := range want {
				if got.Type == w {
					return got, nil
				}
			}
		case <-timer.C:
			return event.Event{}, fmt.Errorf("timed out after %s waiting for %v from %s", askTimeout, want, target)
		case <-ctx.Done():
			return event.Event{}, ctx.Err()
		}
	}
}

// workerOnline reports whether a worker currently holds a bus channel, and
// whether it is a reason worker — the only family wired with ProviderSources,
// so it is the only one that can answer a provider query.
func (s *Server) workerOnline(id string) (online bool, isReason bool) {
	if s.engine.Channel(id) == nil {
		return false, false
	}
	if idt, ok := s.registry.Lookup(id); ok {
		return true, idt.Type == "reason"
	}
	return true, false
}

// providerListResult is the worker.query provider.list answer, reshaped for the
// UI: the selectable providers and the worker's current choice.
type providerListResult struct {
	Providers []providerOption  `json:"providers"`
	Current   providerSelection `json:"current"`
}

type providerOption struct {
	Name    string   `json:"name"`
	Default string   `json:"default"`
	Models  []string `json:"models"`
}

type providerSelection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// handleWorkerProviders answers with a reason worker's selectable providers and
// its current provider/model, by asking the worker itself over the bus.
func (s *Server) handleWorkerProviders(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	online, isReason := s.workerOnline(id)
	if !online {
		http.Error(w, "worker "+id+" is offline", http.StatusServiceUnavailable)
		return
	}
	if !isReason {
		http.Error(w, "worker "+id+" has no switchable providers (only reason workers do)", http.StatusNotFound)
		return
	}

	reply, err := s.ask(r.Context(), id,
		event.New(reasonBase.TypeProviderList, "webui-hiw", nil),
		event.TypeRequestCompleted, event.TypeRequestFailed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if reply.Type == event.TypeRequestFailed {
		http.Error(w, "provider.list rejected", http.StatusBadGateway)
		return
	}

	// The payload is built by the worker, so read it defensively rather than
	// unmarshalling into a struct: a shape change there must not 500 the UI.
	out := providerListResult{}
	current := providerSelection{}
	if cur, ok := reply.Payload["current"].(map[string]any); ok {
		current.Provider, _ = cur["provider"].(string)
		current.Model, _ = cur["model"].(string)
	}
	out.Providers = providersFromPayload(reply.Payload["providers"])
	out.Current = current
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleWorkerSetProvider asks a reason worker to switch its active
// provider/model. Both are required — the worker rejects an empty model rather
// than silently falling back to a default.
func (s *Server) handleWorkerSetProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Provider == "" || body.Model == "" {
		http.Error(w, "provider and model are required", http.StatusBadRequest)
		return
	}

	online, isReason := s.workerOnline(id)
	if !online {
		http.Error(w, "worker "+id+" is offline", http.StatusServiceUnavailable)
		return
	}
	if !isReason {
		http.Error(w, "worker "+id+" has no switchable providers (only reason workers do)", http.StatusNotFound)
		return
	}

	reply, err := s.ask(r.Context(), id,
		event.New(reasonBase.TypeProviderSwitch, "webui-hiw", map[string]any{
			"provider": body.Provider,
			"model":    body.Model,
		}),
		event.TypeRequestCompleted, event.TypeRequestFailed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}

	out := map[string]any{"done": reply.Type == event.TypeRequestCompleted, "provider": body.Provider, "model": body.Model}
	if msg, _ := reply.Payload["error"].(string); msg != "" && msg != "<nil>" {
		out["error"] = msg
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// providersFromPayload reads the providers list out of a request.completed payload.
//
// The value is re-encoded through JSON rather than type-asserted: a managed
// worker runs in this same process, so the event never crosses a serialization
// boundary and the payload still holds the worker's own Go type
// ([]reason.ProviderInfo), not the []any of maps a JSON decode would produce.
// Going through JSON accepts either shape — and any future one — as long as the
// field names match the tags on providerOption.
func providersFromPayload(v any) []providerOption {
	b, err := json.Marshal(v)
	if err != nil {
		return []providerOption{}
	}
	var out []providerOption
	// A null/missing value unmarshals to a nil slice; the UI only shows its
	// empty state for a zero-length list, so normalise it.
	if err := json.Unmarshal(b, &out); err != nil || out == nil {
		return []providerOption{}
	}
	for i := range out {
		if out[i].Models == nil {
			out[i].Models = []string{}
		}
	}
	return out
}

// handleUnmanagedStart/Stop/Restart control external (unmanaged) workers via
// the assembly-provided UnmanagedController.
func (s *Server) handleUnmanagedStart(w http.ResponseWriter, r *http.Request) {
	if s.unmngd == nil {
		http.Error(w, "unmanaged control unavailable", 404)
		return
	}
	if err := s.unmngd.Start(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleUnmanagedStop(w http.ResponseWriter, r *http.Request) {
	if s.unmngd == nil {
		http.Error(w, "unmanaged control unavailable", 404)
		return
	}
	if err := s.unmngd.Stop(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleUnmanagedRestart(w http.ResponseWriter, r *http.Request) {
	if s.unmngd == nil {
		http.Error(w, "unmanaged control unavailable", 404)
		return
	}
	if err := s.unmngd.Restart(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	evt := event.New(event.TypeWorkerAbort, "webui-hiw", map[string]any{
		"worker_id": "webui-hiw",
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
