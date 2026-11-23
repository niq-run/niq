// Package api is one project's data port and its half on the bus.
//
// Over HTTP it serves the project's own JSON and SSE — workers, events,
// approvals, uploads, programs — on a loopback address; the gateway
// authenticates the operator and serves the bundle, then proxies here. It
// serves no SPA and performs no auth.
//
// The other half is iface.Backend (iface.go): the app is the backend of the
// project's interface worker, so it speaks to the human over HTTP and to the
// bus through the handler that worker injects.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niq-run/niq/core/impl/eventbus"
	eventbusapi "github.com/niq-run/niq/core/impl/eventbus/api"
	"github.com/niq-run/niq/core/impl/iface"
	reasonBase "github.com/niq-run/niq/core/impl/reason"
	programBase "github.com/niq-run/niq/core/impl/workers/program"
	workspaceBase "github.com/niq-run/niq/core/impl/workers/workspace"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
)

// ContextInfo tells the single SPA which mode it is in: control (no project
// attached — only project management is available) or project (a specific
// project is attached — talk/events run against it). BusPort carries the
// project's event-bus port so the UI can render a connect configuration for a
// third-party worker (not set in control mode). The host is derived from the
// current page's origin so the URL points at the machine the user reached.
type ContextInfo struct {
	Mode    string `json:"mode"`               // "control" | "project"
	Project string `json:"project,omitempty"`  // project id in project mode
	BusPort int    `json:"bus_port,omitempty"` // project event-bus port
}

// programWorkerID is the bus address of the program worker — the data-plane
// authority for every program (search/describe/read/touch/update/write/delete). The
// WebUI talks to it over the event bus exactly like the worker detail panel
// talks to workspace/reason workers: send a tool event, correlate the
// request.* reply by trace id, and reshape it for the UI.
const programWorkerID = "program"

// ProgramSummary is a read-only summary of one Program for the program
// browser list, reshaped from the program worker's search answer.
type ProgramSummary struct {
	Name        string   `json:"name"`
	ContentType string   `json:"content_type,omitempty"`
	FormType    string   `json:"form_type,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Locked      bool     `json:"locked,omitempty"`
	Contents    int      `json:"contents"` // number of sub-contents
}

// ProgramDetail is a Program's full content view for the program editor,
// reshaped from the program worker's search + load answers.
type ProgramDetail struct {
	Name        string   `json:"name"`
	ContentType string   `json:"content_type,omitempty"`
	FormType    string   `json:"form_type,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Locked      bool     `json:"locked,omitempty"`
	Body        string   `json:"body"`
	Contents    []string `json:"contents,omitempty"` // sub-content paths
}

// UnmanagedStatus is a read-only view of a worker declared in project.json.
// WorkerCreated is the effective result of creating a worker: the identity the
// host resolved plus the placement it was declared with. Credential is
// populated for an unmanaged (dial-in) worker, read from the bus identity so
// the UI can hand the connect configuration to the third party.
type WorkerCreated struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Managed    bool   `json:"managed"`
	Remote     bool   `json:"remote,omitempty"`
	Placement  string `json:"placement,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// WorkerSpawnArgs translates the create form body into the host worker's
// `spawn` arguments (type-specific params flattened to the top level). It
// performs no I/O: creating a worker is a bus call, and the host validates,
// declares and starts in one step. Implemented by the assembly layer; nil
// disables POST /api/workers/create.
type WorkerSpawnArgs interface {
	SpawnArgs(body json.RawMessage) (map[string]any, error)
}

// Server is the WebUI HTTP server.
type Server struct {
	// bus is the interface worker's handler, injected through iface.Backend's
	// SetHandler before that worker starts. Every bus WRITE goes through it.
	busMu sync.RWMutex
	bus   iface.Handler

	server   *http.Server
	listener net.Listener
	addr     string // resolved listen address (host:port), empty until Bind
	devMode  bool   // when true, static assets are proxied to Vite dev server
	eventLog *eventbusapi.EventLog
	engine   *eventbus.Engine
	registry corebus.IdentityRegistry
	catalog  worker.Manifest
	// busControl is the durable identity control plane. ACL edits are a direct
	// control-plane write (identity is not lifecycle, and it does not belong in
	// the bus event world), so the WebUI calls it instead of the host worker.
	busControl corebus.Control

	ctxMu   sync.RWMutex
	context ContextInfo

	// Every worker WRITE is a bus request to the host worker; every worker READ is
	// the injected catalog or registry. Nothing here persists anything, so there
	// is no declaration store or ACL writer to inject.

	// Uploads: projectDir is the on-disk project directory
	// (~/.niq/projects/<id>), uploadDir an optional config override
	// (project.json upload_dir; relative paths resolve against projectDir).
	// The default upload directory is projectDir/uploads.
	projectDir   string
	uploadDirCfg string // project.json upload_dir override
}

// New creates a WebUI Server.
// devMode enables Vite-proxy mode (frontend runs on :5173, APIs stay on addr).
func New(el *eventbusapi.EventLog, engine *eventbus.Engine, catalog worker.Manifest, registry corebus.IdentityRegistry, addr string, devMode bool) *Server {
	s := &Server{eventLog: el, engine: engine, catalog: catalog, registry: registry, devMode: devMode}
	mux := http.NewServeMux()

	// ── API routes ──

	// SSE: real-time event stream (from EventLog, which gets all events via Engine.OnEvent).
	mux.HandleFunc("GET /api/stream", s.serveSSE)

	// Input: publish user input to the bus as HIW.
	mux.HandleFunc("POST /api/input", s.handleInput)

	// Upload: write one file into the project's upload directory and return
	// its absolute path, for the input to reference as a file attachment.
	mux.HandleFunc("POST /api/upload", s.handleUpload)

	// Workers: list all registered workers with identity, connection and
	// host-managed lifecycle state.
	mux.HandleFunc("GET /api/workers", s.handleWorkers)

	// Allow lists: edit a worker's publish/subscribe allow on the bus registry.
	// Both lists are replaced wholesale when present; a missing field keeps the
	// current value. Changes take effect immediately for broadcast routing.
	mux.HandleFunc("PUT /api/workers/{id}/allow", s.handleUpdateAllow)

	// Suspend / resume a host-managed worker (via the host worker's tools).
	mux.HandleFunc("POST /api/workers/{id}/suspend", s.handleSuspend)
	mux.HandleFunc("POST /api/workers/{id}/resume", s.handleResume)

	// Declaration edits (display metadata / construction params): a bus call to
	// the host worker's `update` tool. Display metadata is cosmetic; params are
	// build-time input, so the change applies on the next start.
	mux.HandleFunc("PUT /api/workers/{id}/meta", s.handleUpdateWorkerMeta)
	mux.HandleFunc("PUT /api/workers/{id}/params", s.handleUpdateWorkerParams)

	// Model provider: read and switch a reason worker's active LLM provider.
	// These go over the bus as worker.query / worker.update and wait for the
	// worker's worker.status / worker.updated reply.
	mux.HandleFunc("GET /api/workers/{id}/providers", s.handleWorkerProviders)
	mux.HandleFunc("POST /api/workers/{id}/provider", s.handleWorkerSetProvider)

	// Mounts: read and mutate a workspace worker's mounted directories over
	// the bus (mount.list / mount.add / mount.remove), same ask-reply pattern
	// as the provider endpoints.
	mux.HandleFunc("GET /api/workers/{id}/mounts", s.handleWorkerMounts)
	mux.HandleFunc("POST /api/workers/{id}/mounts/add", s.handleWorkerMountAdd)
	mux.HandleFunc("POST /api/workers/{id}/mounts/remove", s.handleWorkerMountRemove)

	// Start / stop / restart an external (unmanaged) worker. Start also covers
	// declared-but-absent managed workers: they are spawned via the host
	// worker's spawn event.
	mux.HandleFunc("POST /api/workers/{id}/start", s.handleUnmanagedStart)
	mux.HandleFunc("POST /api/workers/{id}/stop", s.handleUnmanagedStop)
	mux.HandleFunc("POST /api/workers/{id}/restart", s.handleUnmanagedRestart)

	// Create a worker on the spot: persist a declaration into project.json and
	// launch it — external workers directly, managed ones via the host worker's
	// spawn event.
	mux.HandleFunc("POST /api/workers/create", s.handleWorkerCreate)

	// Delete a worker: stop it, revoke its bus identity, remove its persisted
	// state, and drop its project.json declaration (unmanaged only).
	mux.HandleFunc("DELETE /api/workers/{id}", s.handleDeleteWorker)

	// Events pagination: load events before a given anchor.
	mux.HandleFunc("GET /api/events/before/{id}", s.handleLoadBefore)

	// Events by request_id: one-shot lookup of a request → response pair.
	mux.HandleFunc("GET /api/events/by-request/{requestId}", s.handleListByRequest)

	// Abort: interrupt a worker's current reasoning.
	mux.HandleFunc("POST /api/abort", s.handleAbort)

	// Send worker an event: publish one of the events the worker declared it
	// responds to (its worker.ready "watch"), as HIW with a client-supplied
	// payload. The bus ACL still applies (HIW must be granted the type).
	mux.HandleFunc("POST /api/workers/{id}/event", s.handleWorkerEvent)

	// Approvals: the HIW owns the approval state (it receives the
	// approval.request events); the server is a thin view — list what HIW
	// tracks, and forward the human's decision through HIW.
	mux.HandleFunc("GET /api/approvals", s.handleApprovals)
	mux.HandleFunc("POST /api/approvals/{id}/decision", s.handleApprovalDecision)

	// Mode context: tells the single SPA whether this WebUI is a control plane
	// or a project instance, and where the control plane lives.
	mux.HandleFunc("GET /api/context", s.handleContext)

	// Programs: browse, read, edit and delete programs via the program
	// worker's bus tools (search/read/touch/update/write/delete).
	mux.HandleFunc("GET /api/programs", s.handleListPrograms)
	mux.HandleFunc("GET /api/programs/{name}", s.handleGetProgram)
	mux.HandleFunc("GET /api/programs/{name}/content", s.handleGetProgramContent)
	mux.HandleFunc("PUT /api/programs/{name}", s.handleUpdateProgram)
	mux.HandleFunc("DELETE /api/programs/{name}", s.handleDeleteProgram)

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

// SetBusControl attaches the durable identity control plane, which ACL edits go
// through. Nil disables PUT /api/workers/{id}/allow.
func (s *Server) SetBusControl(c corebus.Control) {
	s.busControl = c
}

// SetContext records the mode context the single SPA should render in. Safe to
// call from the project assembly after construction and before Start.
func (s *Server) SetContext(ctx ContextInfo) {
	s.ctxMu.Lock()
	defer s.ctxMu.Unlock()
	s.context = ctx
}

// SetProjectDir attaches the on-disk project directory; the default upload
// directory is projectDir/uploads.
func (s *Server) SetProjectDir(dir string) {
	s.projectDir = dir
}

// SetUploadDir overrides the upload directory (project.json upload_dir).
// Relative paths resolve against the project directory.
func (s *Server) SetUploadDir(dir string) {
	s.uploadDirCfg = dir
}

// uploadDir resolves the directory uploaded files are written to: the config
// override when set (made absolute against the project dir), else
// projectDir/uploads. With no project dir known (control mode) it falls back
// to the OS temp dir, where uploads are still functional but out of any
// workspace boundary.
func (s *Server) uploadDir() string {
	if s.uploadDirCfg != "" {
		if filepath.IsAbs(s.uploadDirCfg) {
			return s.uploadDirCfg
		}
		if s.projectDir != "" {
			return filepath.Join(s.projectDir, s.uploadDirCfg)
		}
	}
	if s.projectDir != "" {
		return filepath.Join(s.projectDir, "uploads")
	}
	return filepath.Join(os.TempDir(), "niq-uploads")
}

// programWorkerOnline reports whether the program worker is on the bus. The
// program endpoints degrade gracefully (empty list / offline) when it isn't —
// e.g. a project configured without a program worker.
func (s *Server) programWorkerOnline() bool {
	if s.engine == nil {
		return false
	}
	online, wtype := s.workerOnline(programWorkerID)
	return online && wtype == "program"
}

// askProgram publishes one program-worker tool event (search/read/touch/update/write/
// delete) and waits for the correlated request.completed / request.failed reply,
// exactly as the workspace/reason endpoints do.
func (s *Server) askProgram(ctx context.Context, opts *programTool, args map[string]any) (event.Event, error) {
	req := event.New(opts.Type, "", args)
	if isProgramRead(opts.Type) {
		req.Transient = true // read-only query: streamed live, not durable history
	}
	return s.ask(ctx, programWorkerID, req,
		event.TypeRequestCompleted, event.TypeRequestFailed)
}

// isProgramRead reports whether a program tool is a read-only query (search /
// read). Mutations (touch/update/write/edit/delete) return false and stay durable.
func isProgramRead(t event.EventType) bool {
	return t == programBase.TypeSearch || t == programBase.TypeRead
}

// programTool carries one bus call. The event type IS the tool the worker
// serves (search/read/touch/update/write/edit/delete/run); parseProgram uses
// payload "result".
type programTool struct {
	Type event.EventType
}

// requestFailedErr extracts a program worker's request.failed message, or ""
// when the reply completed.
func requestFailedErr(evt event.Event) string {
	if evt.Type != event.TypeRequestFailed {
		return ""
	}
	if msg, _ := evt.Payload["error"].(string); msg != "" && msg != "<nil>" {
		return msg
	}
	return "request rejected by program worker"
}

// programErrStatus maps a program worker's failure message onto an HTTP status:
// locked → 403, not-found → 404, otherwise 400 (the worker owns the semantic).
func programErrStatus(msg string) int {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "locked"):
		return http.StatusForbidden
	case strings.Contains(lower, "not found"), strings.Contains(lower, "not exist"):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

// parseProgramSearch reshapes the program worker's search answer (a JSON array
// of {name, content_type, form_type, description, tags, locked, contents[]})
// into ProgramSummary rows for the browser list.
type searchResultItem struct {
	Name        string   `json:"name"`
	ContentType string   `json:"content_type,omitempty"`
	FormType    string   `json:"form_type,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Locked      bool     `json:"locked,omitempty"`
	Contents    []string `json:"contents,omitempty"`
}

func (s *Server) parseProgramSearch(evt event.Event, out *[]ProgramSummary) {
	if res := replyResultText(evt); res != "" {
		var items []searchResultItem
		if err := json.Unmarshal([]byte(res), &items); err == nil {
			for _, it := range items {
				*out = append(*out, ProgramSummary{
					Name:        it.Name,
					ContentType: it.ContentType,
					FormType:    it.FormType,
					Description: it.Description,
					Tags:        it.Tags,
					Locked:      it.Locked,
					Contents:    len(it.Contents),
				})
			}
		}
	}
}

// handleListPrograms lists the attached project's programs by asking the
// program worker (search). A project without a program worker gets an empty
// list.
func (s *Server) handleListPrograms(w http.ResponseWriter, r *http.Request) {
	out := []ProgramSummary{}
	if s.programWorkerOnline() {
		reply, err := s.askProgram(r.Context(), &programTool{Type: programBase.TypeSearch}, map[string]any{"query": ""})
		if err == nil {
			s.parseProgramSearch(reply, &out)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleGetProgram returns one program's full detail (metadata + entry body +
// sub-content paths) by asking the program worker search + load.
func (s *Server) handleGetProgram(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.programWorkerOnline() {
		http.Error(w, "program worker offline", http.StatusServiceUnavailable)
		return
	}

	// 1) metadata + sub-contents from search(query=name); pick the exact match.
	searchReply, err := s.askProgram(r.Context(), &programTool{Type: programBase.TypeSearch}, map[string]any{"query": name})
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if msg := requestFailedErr(searchReply); msg != "" {
		http.Error(w, msg, programErrStatus(msg))
		return
	}
	var items []searchResultItem
	if res := replyResultText(searchReply); res != "" {
		_ = json.Unmarshal([]byte(res), &items)
	}
	var found *searchResultItem
	for i := range items {
		if items[i].Name == name {
			found = &items[i]
			break
		}
	}
	if found == nil {
		http.Error(w, "program not found", http.StatusNotFound)
		return
	}

	// 2) entry body from read(path=name).
	loadReply, err := s.askProgram(r.Context(), &programTool{Type: programBase.TypeRead}, map[string]any{"path": name})
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	out := ProgramDetail{
		Name:        found.Name,
		ContentType: found.ContentType,
		FormType:    found.FormType,
		Description: found.Description,
		Tags:        found.Tags,
		Locked:      found.Locked,
		Contents:    found.Contents,
	}
	if msg := requestFailedErr(loadReply); msg != "" {
		http.Error(w, msg, programErrStatus(msg))
		return
	}
	if body := replyResultText(loadReply); body != "" {
		out.Body = body
	}
	if out.Contents == nil {
		out.Contents = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// ProgramContent is one sub-content file's body, reshaped from the program
// worker's read answer for the sub-content viewer.
type ProgramContent struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

// handleGetProgramContent returns one sub-content file's content by asking the
// program worker's read tool for the given path — the same read the detail
// endpoint uses for the entry body. path (a full abstract address like
// "tasks/brief.md") arrives as a query param so it may contain slashes. The
// worker owns the path's validity; a missing/unknown file surfaces as an error.
func (s *Server) handleGetProgramContent(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	if !s.programWorkerOnline() {
		http.Error(w, "program worker offline", http.StatusServiceUnavailable)
		return
	}
	reply, err := s.askProgram(r.Context(), &programTool{Type: programBase.TypeRead}, map[string]any{"path": path})
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if msg := requestFailedErr(reply); msg != "" {
		http.Error(w, msg, programErrStatus(msg))
		return
	}
	body := replyResultText(reply)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ProgramContent{Name: path, Body: body})
}

// handleUpdateProgram replaces a program's metadata and entry body via the
// program worker's update (metadata) + write (entry body) tools. Locked
// programs are refused by the worker.
func (s *Server) handleUpdateProgram(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.programWorkerOnline() {
		http.Error(w, "program worker offline", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		ContentType string   `json:"content_type"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Body        string   `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Metadata first (update), then the entry body (write) — two correlated
	// bus round trips, faithful to the program worker's separation.
	metaArgs := map[string]any{"name": name, "content_type": in.ContentType, "description": in.Description}
	if in.Tags != nil {
		tags := make([]any, 0, len(in.Tags))
		for _, tag := range in.Tags {
			tags = append(tags, tag)
		}
		metaArgs["tags"] = tags
	}
	reply, err := s.askProgram(r.Context(), &programTool{Type: programBase.TypeUpdate}, metaArgs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if msg := requestFailedErr(reply); msg != "" {
		http.Error(w, msg, programErrStatus(msg))
		return
	}

	reply, err = s.askProgram(r.Context(), &programTool{Type: programBase.TypeWrite}, map[string]any{"path": name, "content": in.Body})
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if msg := requestFailedErr(reply); msg != "" {
		http.Error(w, msg, programErrStatus(msg))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleDeleteProgram removes a program and its sub-contents via the program
// worker's delete tool.
func (s *Server) handleDeleteProgram(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.programWorkerOnline() {
		http.Error(w, "program worker offline", http.StatusServiceUnavailable)
		return
	}
	reply, err := s.askProgram(r.Context(), &programTool{Type: programBase.TypeDelete}, map[string]any{"path": name})
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if msg := requestFailedErr(reply); msg != "" {
		http.Error(w, msg, programErrStatus(msg))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetArchived(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.archivedIDs())
}

// archivedIDs returns the ids in the archived set, sorted by id.
func (s *Server) archivedIDs() []string {
	out := []string{}
	if s.catalog == nil {
		return out
	}
	for _, info := range s.catalog.List(true) {
		out = append(out, info.ID)
	}
	sort.Strings(out)
	return out
}

// handleSetArchived toggles whether a worker is archived. The whole move is one
// host operation: the host suspends a live worker, moves the declaration to (or
// back from) the archived file, and drops (or re-ensures) the bus identity under
// its per-worker lock. The UI only names the intent.
func (s *Server) handleSetArchived(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Archived bool `json:"archived"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if _, err := s.callHost(r.Context(), "archive", map[string]any{
		"worker_id": id,
		"archived":  body.Archived,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"archived": s.archivedIDs()})
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
		WorkerIDs:           r.URL.Query()["worker"],
		WorkerRoles:         parseWorkerRoles(r.URL.Query()),
		TraceID:             r.URL.Query().Get("trace"),
		RequestID:           r.URL.Query().Get("request"),
		Type:                event.EventType(r.URL.Query().Get("type")),
		Types:               cleanTypeList(r.URL.Query()["type"]),
		ExcludeTypes:        cleanTypeList(r.URL.Query()["exclude"]),
		ExcludeTypePrefixes: cleanTypeList(r.URL.Query()["exclude_prefix"]),
		KeepTypes:           cleanTypeList(r.URL.Query()["keep"]),
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Connection-lifecycle diagnostic: log how long this SSE lived before it
	// ended, so we can tell whether the stream was closed server-side (a normal
	// ctx close or an error below) or just vanished from under us (a client /
	// proxy disconnect the server never saw). Alive duration + the connect time
	// let us match it to a browser-side "interrupted" console error.
	started := time.Now()
	var sentEvents, sentPings int
	closedReason := "unknown"
	defer func() {
		log.Printf(
			"[webui] SSE closed path=%s alive=%s reason=%s events=%d pings=%d",
			r.URL.Path, time.Since(started).Round(time.Second), closedReason, sentEvents, sentPings,
		)
	}()

	// The stream now carries only events newer than `watermark`; history is
	// paged in separately by the client. Advertise the watermark up front as a
	// control event so the client can start its backwards pagination, then
	// deliver the watermark event itself — it is in no history page
	// (LoadBefore is strictly-before) and was routed before the subscription,
	// so this is its only delivery path. Without it the newest event at
	// connect time is invisible until a later reconnect covers it.
	ch, watermarkID, gapEvt, err := s.eventLog.FollowLive(r.Context(), filter)
	if err != nil {
		log.Printf("[webui] follow error: %v", err)
		return
	}

	fmt.Fprintf(w, "event: watermark\ndata: %s\n\n", watermarkID)
	flusher.Flush()
	if gapEvt.ID != "" {
		data, _ := json.Marshal(gapEvt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Heartbeat: an idle stream sends nothing, so a silently-dead connection
	// (laptop sleep, NAT timeout) stays undetected and the browser keeps
	// waiting on a stale EventSource. A periodic comment keeps the path warm
	// and makes drops surface as a prompt reconnect.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				// The event stream closed. A canceled request context means the
				// client (or a proxy in front of it) went away; otherwise the
				// event log itself ended (bus / project shutdown) and every
				// browser on this stream reconnects at the same moment.
				if r.Context().Err() != nil {
					closedReason = "client_disconnected"
				} else {
					closedReason = "eventlog_ended"
				}
				return
			}
			sentEvents++
			data, _ := json.Marshal(evt)
			// A broken pipe surfaces as an error on the next write (http.Flusher
			// has no error itself); that is how a client / proxy disconnect that
			// the server never noticed shows up.
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				closedReason = "write_failed"
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			sentPings++
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				closedReason = "write_failed"
				return
			}
			flusher.Flush()
		}
	}
}

// ── API handlers ──

// uploadMaxBytes caps one uploaded file. Files travel on disk (only their
// path enters the input), so the cap is generous; images embedded as base64
// are capped client-side at 5MB instead.
const uploadMaxBytes = 64 << 20

// inputMaxBytes caps a /api/input body: up to 4 image attachments of 5MB
// each base64-expand to ~27MB, plus text.
const inputMaxBytes = 32 << 20

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, inputMaxBytes)
	var body struct {
		Content []map[string]any `json:"content"`
		// Targets addresses the message to one or more workers (the input's
		// @mentions). Target is the pre-multi-target single spelling still sent
		// by older clients / scripts.
		Targets   []string `json:"targets,omitempty"`
		Target    string   `json:"target,omitempty"`
		InputMode string   `json:"input_mode,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	targets := body.Targets
	if len(targets) == 0 && body.Target != "" {
		targets = []string{body.Target}
	}
	// The input is always addressed to specific workers, so an empty target list
	// is a client bug (or a client/server build mismatch). It must NOT fall
	// through to a broadcast: that would deliver the message to every worker
	// subscribed to worker.input, turning a bug into “I only @-ed one worker but
	// everyone got it”.
	if len(targets) == 0 {
		http.Error(w, "input requires at least one target", http.StatusBadRequest)
		return
	}
	if err := s.sendInput(r.Context(), body.Content, targets, body.InputMode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleUpload serves POST /api/upload: one multipart file ("file" field) is
// written into the project's upload directory under a sanitized, collision-
// free name, and its absolute path is returned for the input to reference.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, uploadMaxBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "invalid multipart body: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing multipart file field \"file\"", http.StatusBadRequest)
		return
	}
	defer file.Close()

	dir := s.uploadDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "create upload dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	name := sanitizeFileName(header.Filename)
	dst, err := os.CreateTemp(dir, "up-*-"+name)
	if err != nil {
		http.Error(w, "create file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	written, err := io.Copy(dst, file)
	if err != nil {
		dst.Close()
		os.Remove(dst.Name())
		http.Error(w, "write file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := dst.Close(); err != nil {
		os.Remove(dst.Name())
		http.Error(w, "close file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The temp file (up-<rand>-<name>) already carries a collision-free base
	// name, so it doubles as the final destination — the rename below is a
	// same-path no-op kept for clarity/portability. The temp file must NOT be
	// removed once uploaded, only on the failure paths above.
	final := filepath.Join(dir, filepath.Base(dst.Name()))
	if err := os.Rename(dst.Name(), final); err != nil {
		os.Remove(dst.Name())
		http.Error(w, "finalize file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"path": final,
		"name": header.Filename,
		"size": written,
	})
}

// sanitizeFileName strips anything path-like or unprintable from a client
// filename, keeping only its base and a conservative character set; empty
// names become "file".
func sanitizeFileName(name string) string {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if base == "." || base == "/" || base == "" {
		return "file"
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "file"
	}
	return out
}

// WorkerView is the unified view of a worker: its registered identity (from
// the bus registry), its connection status, and — if host-managed — its
// lifecycle state, or if external — its supervision state. Tags and a
// description (from the worker declaration) ride along for display.
type WorkerView struct {
	ID             string                 `json:"id"`
	Type           string                 `json:"type"`
	Label          string                 `json:"label,omitempty"`
	Tags           []string               `json:"tags,omitempty"`
	Description    string                 `json:"description,omitempty"`
	Params         map[string]any         `json:"params,omitempty"`
	Credential     string                 `json:"credential,omitempty"`
	PublishAllow   []event.PublishPattern `json:"publish_allow,omitempty"`
	SubscribeAllow []event.EventPattern   `json:"subscribe_allow,omitempty"`
	Online         bool                   `json:"online"`
	// Placement is where the worker runs: in-process, managed-process or
	// unmanaged-process. Managed/Unmanaged below are the UI's older spelling of
	// the same distinction and stay for the existing frontend.
	Placement      string `json:"placement,omitempty"`
	Managed        bool   `json:"managed"`
	State          string `json:"state,omitempty"` // managed: "running" | "suspended" | "stopped" (stopped = declared but not instantiated)
	Unmanaged      bool   `json:"unmanaged,omitempty"`
	UnmanagedState string `json:"unmanaged_state,omitempty"` // "running" | "stopped"
	// Remote marks a third-party worker that connects on its own; it has no
	// local process lifecycle (no start/stop).
	Remote bool `json:"remote,omitempty"`
}

// handleWorkers returns every registered worker identity merged with its
// connection status and host-managed lifecycle state.
func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	online := map[string]bool{}
	for _, id := range s.engine.OnlineWorkers() {
		online[id] = true
	}

	// The manifest catalog is the authority on who is declared, where each
	// worker runs, what state its owner reports, and the display metadata.
	// Registry entries supply the identity details (credential, ACL, online).
	catalog := map[string]worker.WorkerInfo{}
	var infos []worker.WorkerInfo
	if s.catalog != nil {
		infos = s.catalog.List(false)
		for _, info := range infos {
			catalog[info.ID] = info
		}
	}

	applyCatalog := func(v *WorkerView) {
		info, ok := catalog[v.ID]
		if !ok {
			return
		}
		v.Label, v.Tags, v.Description = info.Label, info.Tags, info.Description
		if v.Params == nil {
			v.Params = info.Params
		}
		v.Placement = string(info.Placement)
		switch info.Placement {
		case worker.PlacementInProcess:
			v.Managed = true
			v.State = info.State
		case worker.PlacementManagedProcess:
			v.Unmanaged = true
			v.UnmanagedState = info.State
		case worker.PlacementUnmanagedProcess:
			v.Unmanaged = true
			v.UnmanagedState = info.State
			v.Remote = true
		}
		if v.Type == "" {
			v.Type = info.Type
		}
	}

	var views []WorkerView
	seen := map[string]bool{}
	for _, id := range s.registry.List() {
		v := WorkerView{ID: id.WorkerID, Type: id.Type, Credential: id.Credential,
			PublishAllow: id.PublishAllow, SubscribeAllow: id.SubscribeAllow,
			Online: online[id.WorkerID]}
		applyCatalog(&v)
		seen[id.WorkerID] = true
		views = append(views, v)
	}

	// Include declared workers with no registry identity yet: a never-started
	// worker (the UI offers a start button) and a spawn in flight.
	for _, info := range infos {
		if seen[info.ID] {
			continue
		}
		v := WorkerView{ID: info.ID, Type: info.Type, Online: online[info.ID]}
		applyCatalog(&v)
		views = append(views, v)
	}

	// The registry's List is already ID-sorted; sort the merged view too, so
	// transient workers land at a deterministic position instead of trailing in
	// an ever-changing insertion order.
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })

	json.NewEncoder(w).Encode(views)
}

// declaredIn reports whether the catalog knows the worker, so an offline but
// declared worker can still have its grants edited (the next start registers the
// identity and picks the persisted grant up).
func declaredIn(c worker.Manifest, id string) bool {
	_, ok, err := c.Get(id)
	return err == nil && ok
}

// handleUpdateAllow edits a worker's allow lists on the bus registry. The
// request may carry either or both lists; a missing one keeps the current
// value, so callers can edit just subscribe_allow (or just publish_allow)
// without echoing the other back. SubscribeAllow patterns support the
// optional source restriction ({"type","source_id"}).
func (s *Server) handleUpdateAllow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idt, online := s.registry.Lookup(id)
	if !online && (s.catalog == nil || !declaredIn(s.catalog, id)) {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	var req struct {
		PublishAllow   []event.PublishPattern `json:"publish_allow"`
		SubscribeAllow []event.EventPattern   `json:"subscribe_allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	pubAllow := req.PublishAllow
	subAllow := req.SubscribeAllow
	if online {
		if pubAllow == nil {
			pubAllow = idt.PublishAllow
		}
		if subAllow == nil {
			subAllow = idt.SubscribeAllow
		}
	} else if pubAllow == nil || subAllow == nil {
		http.Error(w, "an offline worker requires both publish_allow and subscribe_allow", http.StatusBadRequest)
		return
	}
	if subAllow != nil {
		// Store the canonical spelling: an omitted source means "any source".
		out := make([]event.EventPattern, 0, len(subAllow))
		for _, p := range subAllow {
			if p.SourceID == "" {
				p.SourceID = "*"
			}
			out = append(out, p)
		}
		subAllow = out
	}

	// One write, one owner: the bus control plane persists the grant and
	// projects it onto the runtime registry in that order.
	if s.busControl == nil {
		http.Error(w, "ACL editing is not configured", http.StatusServiceUnavailable)
		return
	}
	// A declared worker that never started has no identity record yet. Materialize
	// it here — the manifest knows its type and placement — so the grant can be
	// written before the worker's first start.
	if _, ok := s.busControl.Lookup(id); !ok {
		info, ok, err := s.catalog.Get(id)
		if err != nil {
			http.Error(w, "read worker: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "worker not found", http.StatusNotFound)
			return
		}
		if err := s.busControl.Register(corebus.Identity{
			WorkerID: id,
			Type:     info.Type,
			Remote:   info.Placement != worker.PlacementInProcess,
		}); err != nil {
			http.Error(w, "create identity: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	if err := s.busControl.UpdateACL(id, pubAllow, subAllow); err != nil {
		http.Error(w, "update allow: "+err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("[webui] updated allow lists for %s", id)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleSuspend suspends a worker by sending the host worker's suspend event
// (data-plane, auditable) and reports the outcome.
func (s *Server) handleSuspend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.callHost(r.Context(), "suspend", map[string]any{"worker_id": id}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleResume starts a declared worker via the host worker's resume event —
// the same request the start button sends.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.callHost(r.Context(), "resume", map[string]any{"worker_id": id}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
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
// replyResultText renders a request.completed "result" as text: strings
// verbatim, structured values (the bus now carries replies as JSON, not as
// pre-encoded strings) as their JSON. Callers that only need the text keep
// working, and they no longer have to care which shape the worker sent.
func replyResultText(evt event.Event) string {
	return event.ReplyText(evt.Payload)
}

func (s *Server) ask(ctx context.Context, target string, evt event.Event, want ...event.EventType) (event.Event, error) {
	h, err := s.busHandler()
	if err != nil {
		return event.Event{}, err
	}
	if evt.WorkerId == "" {
		evt.WorkerId = h.ID() // the caller IS this worker; the bus stamps the sender
	}
	// Correlation (trace/request ids, the wait, the timeout) belongs to the
	// interface worker: the reply is an ordinary event addressed to it, and only
	// the side holding the channel can pair it.
	reply, err := h.Request(ctx, target, evt)
	if err != nil {
		// A failed/rejected reply is still a REPLY: callers turn it into a message
		// for the user, so hand it back instead of only the error.
		var rerr *iface.ReplyError
		if errors.As(err, &rerr) {
			return reply, nil
		}
		return event.Event{}, err
	}
	return reply, nil
}

// workerOnline reports whether a worker currently holds a bus channel, and
// its registered type — callers gate on the family they need (reason for
// provider.*, workspace for mount.*).
func (s *Server) workerOnline(id string) (online bool, wtype string) {
	if s.engine.Channel(id) == nil {
		return false, ""
	}
	if idt, ok := s.registry.Lookup(id); ok {
		return true, idt.Type
	}
	return true, ""
}

// isReasonFamilyType reports whether a worker runs on the shared reasoning
// mechanism and therefore exposes provider.* management: the generic reason
// worker and the coding reason worker.
func isReasonFamilyType(workerType string) bool {
	switch workerType {
	case "reason", "coding":
		return true
	}
	return false
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
	online, wtype := s.workerOnline(id)
	if !online {
		http.Error(w, "worker "+id+" is offline", http.StatusServiceUnavailable)
		return
	}
	if !isReasonFamilyType(wtype) {
		http.Error(w, "worker "+id+" has no switchable providers (only reason-family workers do)", http.StatusNotFound)
		return
	}

	req := event.New(reasonBase.TypeProviderList, "", nil)
	req.Transient = true // read-only query
	reply, err := s.ask(r.Context(), id, req,
		event.TypeRequestCompleted, event.TypeRequestFailed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if reply.Type == event.TypeRequestFailed {
		http.Error(w, "provider.list rejected", http.StatusBadGateway)
		return
	}

	// The reply's "result" is the JSON snapshot the worker marshaled (see
	// handleStatusQuery). Parse defensively: a shape change there must not
	// 500 the UI.
	out := providerListResult{}
	if res := replyResultText(reply); res != "" {
		var structured providerListResult
		if err := json.Unmarshal([]byte(res), &structured); err == nil {
			out = structured
		}
	}
	if out.Providers == nil {
		out.Providers = []providerOption{}
	}
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

	online, wtype := s.workerOnline(id)
	if !online {
		http.Error(w, "worker "+id+" is offline", http.StatusServiceUnavailable)
		return
	}
	if !isReasonFamilyType(wtype) {
		http.Error(w, "worker "+id+" has no switchable providers (only reason-family workers do)", http.StatusNotFound)
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

// mountListResult is the mount.list answer, reshaped for the UI: the mounted
// directories (the first is the primary mount).
type mountListResult struct {
	Mounts  []string `json:"mounts"`
	Primary string   `json:"primary,omitempty"`
}

// mountListResult parses a reply payload's "result" JSON (the snapshot the
// workspace worker marshals) into out. Parse defensively: a shape change in
// the worker must not 500 the UI.
func parseMountResult(reply event.Event, out *mountListResult) {
	if res := replyResultText(reply); res != "" {
		var structured mountListResult
		if err := json.Unmarshal([]byte(res), &structured); err == nil {
			*out = structured
		}
	}
	if out.Mounts == nil {
		out.Mounts = []string{}
	}
}

// requireFamily gates a bus-ask endpoint on the worker being online and of
// the given type (the family that serves the event).
func (s *Server) requireFamily(w http.ResponseWriter, id, family string) bool {
	online, wtype := s.workerOnline(id)
	if !online {
		http.Error(w, "worker "+id+" is offline", http.StatusServiceUnavailable)
		return false
	}
	if wtype != family {
		http.Error(w, "worker "+id+" is a "+wtype+" worker, not a "+family+" worker", http.StatusNotFound)
		return false
	}
	return true
}

// handleWorkerMounts answers with a workspace worker's mounted directories by
// asking the worker itself over the bus (mount.list).
func (s *Server) handleWorkerMounts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireFamily(w, id, "workspace") {
		return
	}

	req := event.New(workspaceBase.TypeMountList, "webui-hiw", nil)
	req.Transient = true // read-only query
	reply, err := s.ask(r.Context(), id, req,
		event.TypeRequestCompleted, event.TypeRequestFailed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if reply.Type == event.TypeRequestFailed {
		http.Error(w, "mount.list rejected", http.StatusBadGateway)
		return
	}

	out := mountListResult{}
	parseMountResult(reply, &out)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleWorkerMountAdd / handleWorkerMountRemove ask a workspace worker to
// mount / unmount a directory. add is approval-gated worker-side: the
// workspace's default approver is this UI's HIW (webui-hiw), so a UI request
// applies directly; a project with a different approver parks it behind an
// approval and ask() times out — surfaced as a gateway timeout.
func (s *Server) handleWorkerMountAdd(w http.ResponseWriter, r *http.Request) {
	s.workerMountMutate(w, r, workspaceBase.TypeMountAdd)
}

func (s *Server) handleWorkerMountRemove(w http.ResponseWriter, r *http.Request) {
	s.workerMountMutate(w, r, workspaceBase.TypeMountRemove)
}

func (s *Server) workerMountMutate(w http.ResponseWriter, r *http.Request, evtType event.EventType) {
	id := r.PathValue("id")
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Path) == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if !s.requireFamily(w, id, "workspace") {
		return
	}

	reply, err := s.ask(r.Context(), id,
		event.New(evtType, "webui-hiw", map[string]any{"path": body.Path}),
		event.TypeRequestCompleted, event.TypeRequestFailed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}

	out := map[string]any{"done": reply.Type == event.TypeRequestCompleted, "path": body.Path}
	if msg, _ := reply.Payload["error"].(string); msg != "" && msg != "<nil>" {
		out["error"] = msg
	}
	// A successful mutate replies with the fresh mount snapshot — pass it on
	// so the UI re-renders without a second round trip.
	mounts := mountListResult{}
	parseMountResult(reply, &mounts)
	out["mounts"] = mounts.Mounts
	out["primary"] = mounts.Primary
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// hostWorkerID is the worker that owns lifecycles. Every lifecycle write in
// this file is a bus request to it — the WebUI has no privileged path of its
// own, so an action taken by a human and one taken by an agent are the same
// operation, auditable in the same event stream.
const hostWorkerID = "host"

// lifecycleTimeout bounds a lifecycle request. Destroy of a managed-process
// worker waits for the child to be reaped, so it needs more than a metadata
// round trip.
const lifecycleTimeout = 30 * time.Second

// callHost sends a lifecycle event to the host worker and waits for its
// terminal reply. It returns the reply payload for callers that need it.
func (s *Server) callHost(ctx context.Context, evtType event.EventType, payload map[string]any) (map[string]any, error) {
	if s.engine == nil || s.engine.Channel(hostWorkerID) == nil {
		return nil, fmt.Errorf("the host worker is offline; the declaration is persisted — retry once it is back")
	}
	ctx, cancel := context.WithTimeout(ctx, lifecycleTimeout)
	defer cancel()
	h, err := s.busHandler()
	if err != nil {
		return nil, err
	}
	reply, err := s.ask(ctx, hostWorkerID, event.New(evtType, h.ID(), payload),
		event.TypeRequestCompleted, event.TypeRequestFailed)
	if err != nil {
		return nil, err
	}
	if reply.Type == event.TypeRequestFailed {
		msg, _ := reply.Payload["error"].(string)
		if msg == "" {
			msg = "rejected"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	out := map[string]any{}
	for k, v := range reply.Payload {
		out[k] = v
	}
	return out, nil
}

// handleUnmanagedStart starts a declared worker through the host's `resume`:
// an in-process worker is built from its declaration, a managed-process worker
// is launched, an unmanaged-process worker gets its identity re-ensured. The
// name is historical — the endpoint now covers every placement.
func (s *Server) handleUnmanagedStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.callHost(r.Context(), "resume", map[string]any{"worker_id": id}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleUnmanagedStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.callHost(r.Context(), "suspend", map[string]any{"worker_id": id}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleUnmanagedRestart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.callHost(r.Context(), "restart", map[string]any{"worker_id": id}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleWorkerCreate creates a worker through the host worker's `spawn` tool —
// the same single step an agent uses: validate, declare and start. The form body
// IS the spawn argument object (a nested `params` object is accepted and
// flattened by the host), so nothing is translated or persisted here.
func (s *Server) handleWorkerCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	args := map[string]any{}
	if err := json.Unmarshal(body, &args); err != nil {
		http.Error(w, "bad worker definition: "+err.Error(), http.StatusBadRequest)
		return
	}

	reply, err := s.callHost(r.Context(), "spawn", args)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	created := WorkerCreated{}
	created.ID, _ = reply["worker_id"].(string)
	created.Type, _ = reply["type"].(string)
	created.Placement, _ = reply["placement"].(string)
	switch worker.Placement(created.Placement) {
	case worker.PlacementInProcess:
		created.Managed = true
	case worker.PlacementUnmanagedProcess:
		created.Remote = true
	}
	// A dial-in worker's credential is minted by the spawn; read it from the bus
	// identity (never from the host's reply, which deliberately carries no
	// secrets) so the UI can hand out the connect configuration.
	if created.Remote && s.registry != nil {
		if idt, ok := s.registry.Lookup(created.ID); ok {
			created.Credential = idt.Credential
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(created)
}

// handleUpdateWorkerMeta edits a worker's display metadata (label / tags /
// description) through the host's `update` tool. The body carries every field,
// so an empty value clears it.
func (s *Server) handleUpdateWorkerMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Label       string   `json:"label"`
		Tags        []string `json:"tags"`
		Description string   `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	tags := in.Tags
	if tags == nil {
		tags = []string{}
	}
	if _, err := s.callHost(r.Context(), "update", map[string]any{
		"worker_id":   id,
		"label":       in.Label,
		"tags":        tags,
		"description": in.Description,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleUpdateWorkerParams edits a worker's construction params through the
// host's `update` tool. Params are build-time input: nothing live is touched, so
// the edit takes effect on the next start. Keeping it out of the running worker
// also means a malformed edit cannot take the worker down before it is reverted.
func (s *Server) handleUpdateWorkerParams(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Params == nil {
		in.Params = map[string]any{}
	}
	if _, err := s.callHost(r.Context(), "update", map[string]any{
		"worker_id": id,
		"params":    in.Params,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "params": in.Params})
}

// handleDeleteWorker permanently removes a worker: it stops the live process
// (managed worker / unmanaged subprocess), disconnects it from the bus, revokes
// its registry identity, deletes its persisted state, and (for unmanaged
// workers) drops its project.json declaration so it is not relaunched.
func (s *Server) handleDeleteWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Deleting is a lifecycle operation like any other: the host stops the
	// worker, drops its declaration and persisted state, disconnects it and
	// revokes its identity.
	if _, err := s.callHost(r.Context(), "destroy", map[string]any{"worker_id": id}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	h, err := s.busHandler()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	evt := event.New(event.TypeWorkerAbort, h.ID(), map[string]any{
		"worker_id": h.ID(),
	})
	if body.Target != "" {
		_ = h.Send(r.Context(), evt, body.Target)
	} else {
		_ = h.Broadcast(r.Context(), evt)
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleWorkerEvent serves POST /api/workers/{id}/event: the human UI drives a
// worker's behaviour/state by publishing an event from the worker's "watch"
// contract. The payload is the argument object (top-level). The event
// is sent through the normal bus: HIW's publish_allow must grant the type, and
// the target worker must be online.
func (s *Server) handleWorkerEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Type == "" {
		http.Error(w, "event type is required", http.StatusBadRequest)
		return
	}
	if s.engine.Channel(id) == nil {
		http.Error(w, "worker "+id+" is offline", http.StatusServiceUnavailable)
		return
	}
	reqID, err := s.sendEvent(r.Context(), event.EventType(body.Type), id, body.Payload)
	if err != nil {
		http.Error(w, "send event: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"request_id": reqID})
}

// handleApprovals serves GET /api/approvals: the approval entries the HIW
// tracks (pending first, then the decided history). The server holds no state
// of its own — the HIW is the owner.
func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	entries := s.approvals()
	if entries == nil {
		entries = []ApprovalEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"approvals": entries})
}

// handleApprovalDecision serves POST /api/approvals/{id}/decision: forward the
// human's verdict through HIW to the requesting worker. The entry is looked up
// by its approval.request event id; the requester must still be online.
func (s *Server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Approved *bool  `json:"approved"`
		Note     string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Approved == nil {
		http.Error(w, "approved is required", http.StatusBadRequest)
		return
	}

	var entry ApprovalEntry
	found := false
	for _, e := range s.approvals() {
		if e.EventID == id {
			entry = e
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "unknown approval "+id, http.StatusNotFound)
		return
	}
	if s.engine.Channel(entry.WorkerID) == nil {
		http.Error(w, "worker "+entry.WorkerID+" is offline", http.StatusServiceUnavailable)
		return
	}
	if err := s.sendApprovalDecision(r.Context(), entry.RequestID, *body.Approved, body.Note); err != nil {
		http.Error(w, "send decision: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"done": true})
}

// parseWorkerRoles reads the ?role= multi values into worker-traffic roles
// (event.RoleSent / event.RoleReceived). Unknown values are dropped; an empty
// result means both roles match.
func parseWorkerRoles(q url.Values) []string {
	var roles []string
	for _, r := range q["role"] {
		if (r == event.RoleSent || r == event.RoleReceived) && !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	return roles
}

// cleanTypeList returns the non-empty values of the ?type multi-value query
// parameters — the event-type whitelist (e.g. the talk view's response-only
// mode). Returns nil (no type filter) when empty.
func cleanTypeList(raw []string) []string {
	var types []string
	for _, t := range raw {
		t = strings.TrimSpace(t)
		if t != "" && !slices.Contains(types, t) {
			types = append(types, t)
		}
	}
	return types
}

func (s *Server) handleLoadBefore(w http.ResponseWriter, r *http.Request) {
	anchor := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	filter := eventbusapi.Filter{
		WorkerIDs:           r.URL.Query()["worker"],
		WorkerRoles:         parseWorkerRoles(r.URL.Query()),
		TraceID:             r.URL.Query().Get("trace"),
		RequestID:           r.URL.Query().Get("request"),
		Type:                event.EventType(r.URL.Query().Get("type")),
		Types:               cleanTypeList(r.URL.Query()["type"]),
		ExcludeTypes:        cleanTypeList(r.URL.Query()["exclude"]),
		ExcludeTypePrefixes: cleanTypeList(r.URL.Query()["exclude_prefix"]),
		KeepTypes:           cleanTypeList(r.URL.Query()["keep"]),
	}

	events, err := s.eventLog.LoadBefore(r.Context(), filter, anchor, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(events)
}

// handleListByRequest returns all persisted events sharing a request_id: the
// invocation and its request.* reply. The detail panel calls this to jump from
// a tool-call to its answer (or back) — a one-shot query, not a live filter.
func (s *Server) handleListByRequest(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("requestId")
	events, err := s.eventLog.ListByRequest(r.Context(), requestID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The log returns nil when nothing matched; encode an empty array so the
	// client always parses a list (a JSON null breaks the callers' .find).
	if events == nil {
		events = []event.Event{}
	}
	json.NewEncoder(w).Encode(events)
}

// ── CORS ──

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
