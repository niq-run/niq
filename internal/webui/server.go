// Package webui provides a web-based human interface for niq.
//
// It serves a React SPA that communicates with the backend via HTTP and SSE.
// It is owned by the project/control binaries, not by the HIW worker — the
// HTTP server directly references HIW (for sending input) and EventLog (for
// streaming).
package webui

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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

	"github.com/google/uuid"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/store"
	"github.com/niq-run/niq/pkg/eventbus"
	eventbusapi "github.com/niq-run/niq/pkg/eventbus/api"
	reasonBase "github.com/niq-run/niq/pkg/reason"
	"github.com/niq-run/niq/pkg/services/workerhost"
	"github.com/niq-run/niq/pkg/workers/hiw"
	workspaceBase "github.com/niq-run/niq/pkg/workers/workspace"
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

// ProgramSummary is a read-only summary of one Program under the attached
// project — what the WebUI program browser lists. It mirrors
// project.ProgramView.
//
// ProgramLister returns a project's program summaries. It is implemented by
// the assembly layer (backed by the project's programs/ directory); nil
// disables the endpoint (it replies with an empty list).
type ProgramSummary struct {
	Name        string   `json:"name"`
	ContentType string   `json:"content_type,omitempty"`
	FormType    string   `json:"form_type,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Locked      bool     `json:"locked,omitempty"`
	Contents    int      `json:"contents,omitempty"`
}

type ProgramLister interface {
	ListPrograms() ([]ProgramSummary, error)
}

// UnmanagedStatus is a read-only view of a worker declared in project.json.
// Managed flags host-managed declarations; the state of a managed declaration
// is always "stopped" — only the worker service knows a live lifecycle.
type UnmanagedStatus struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Managed bool   `json:"managed"`
	State   string `json:"state"` // "running" | "stopped"
	Alive   bool   `json:"alive"`
}

// WorkerCreated is the effective result of creating a worker declaration: the
// persisted identity plus, for a managed worker, the bus "spawn" event payload
// the server sends to the host worker (external workers are started by the
// creator itself before it returns).
type WorkerCreated struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Managed bool           `json:"managed"`
	Spawn   map[string]any `json:"-"`
}

// WorkerDeclCreator creates a worker declaration on the spot from a
// project.WorkerConfig-shaped JSON body: it persists the declaration into
// project.json and launches it — external workers directly, managed ones via
// the returned Spawn payload. Implemented by the assembly layer; nil disables
// POST /api/workers/create.
type WorkerDeclCreator interface {
	Create(body json.RawMessage) (WorkerCreated, error)
	// ManagedSpawn returns the spawn-event payload for a declared managed
	// worker (built from its project.json declaration). managed is false when
	// the id is not a declared managed worker.
	ManagedSpawn(id string) (spawn map[string]any, managed bool, err error)
}

// UnmanagedController controls external (unmanaged) workers. Implemented by
// the project assembly layer; nil disables the endpoints. List returns the
// workers the controller currently supervises; Declared returns every worker
// declared as external in project.json (including ones not yet started), so
// the UI can offer a start button for declared-but-idle workers.
type UnmanagedController interface {
	Start(id string) error
	Stop(id string) error
	Restart(id string) error
	List() []UnmanagedStatus
	Declared() []UnmanagedStatus
	// Remove stops the worker and removes its declaration from project.json
	// so it is not relaunched on project restart. It does not touch the bus
	// registry identity or persisted state dirs.
	Remove(id string) error
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

	ctxMu       sync.RWMutex
	context     ContextInfo
	archived    ArchivedStore
	programs    ProgramLister
	unmngd      UnmanagedController
	declCreator WorkerDeclCreator
	declRemover WorkerDeclRemover

	// Uploads: projectDir is the on-disk project directory
	// (~/.niq/projects/<id>), uploadDir an optional config override
	// (project.json upload_dir; relative paths resolve against projectDir).
	// The default upload directory is projectDir/uploads.
	projectDir   string
	uploadDirCfg string // project.json upload_dir override
	coverageMu   sync.Mutex
	coverage     *uploadCoverage // last mount-coverage check (cached a few seconds)

	// Optional basic auth: required for requests coming from non-loopback peers.
	// Localhost (same machine) access stays open. Both must be set to enable.
	authUser string
	authPass string
}

// uploadCoverage caches whether any online workspace worker's mounts contain
// the upload directory — asking over the bus on every input would be wasteful.
type uploadCoverage struct {
	at      time.Time
	covered bool
}

// uploadCoverageTTL bounds how stale the cached coverage answer may get.
const uploadCoverageTTL = 10 * time.Second

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

	// Programs: browse the programs under the attached project.
	mux.HandleFunc("GET /api/programs", s.handleListPrograms)

	// Archived workers: which workers are hidden from the selector (by default),
	// and toggling that flag (persisted in the project.json worker definitions).
	mux.HandleFunc("GET /api/archived", s.handleGetArchived)
	mux.HandleFunc("POST /api/workers/{id}/archived", s.handleSetArchived)

	s.server = &http.Server{Addr: addr, Handler: cors(s.basicAuth(mux))}
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

// WorkerDeclRemover removes a worker's project.json declaration. Implemented by
// the assembly layer (which owns the project id); nil means managed-worker
// deletes do not edit project.json.
type WorkerDeclRemover interface {
	RemoveDecl(id string) error
}

// SetWorkerDeclRemover attaches the project-declaration remover used when
// deleting a managed worker.
func (s *Server) SetWorkerDeclRemover(r WorkerDeclRemover) {
	s.declRemover = r
}

// SetWorkerDeclCreator attaches the declaration creator (nil disables
// POST /api/workers/create and the managed branch of the start endpoint).
func (s *Server) SetWorkerDeclCreator(c WorkerDeclCreator) {
	s.declCreator = c
}

// SetProgramLister attaches the per-project program lister backing the
// program browser (nil disables the endpoint — it then replies empty).
func (s *Server) SetProgramLister(l ProgramLister) {
	s.programs = l
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

// SetBasicAuth enables HTTP basic auth for requests from non-loopback peers
// (local machine access stays password-free). Cleared when either user or pass
// is empty.
func (s *Server) SetBasicAuth(user, pass string) {
	s.authUser, s.authPass = user, pass
}

// ParseAuthSpec parses a "user:password" spec into separate parts. A value
// without a colon is treated as the password with a default user of "niq".
// Returns ok=false for an empty/blank spec or when parts would be empty (auth
// stays disabled).
func ParseAuthSpec(spec string) (user, pass string, ok bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", false
	}
	user = "niq"
	pass = spec
	if i := strings.Index(spec, ":"); i >= 0 {
		user, pass = spec[:i], spec[i+1:]
	}
	if user == "" || pass == "" {
		return "", "", false
	}
	return user, pass, true
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

// handleListPrograms returns the programs under the attached project (empty
// list when no lister is wired or the project has no program space).
func (s *Server) handleListPrograms(w http.ResponseWriter, r *http.Request) {
	var list []ProgramSummary = []ProgramSummary{}
	if s.programs != nil {
		if ps, err := s.programs.ListPrograms(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		} else if len(ps) > 0 {
			list = ps
		}
	}
	json.NewEncoder(w).Encode(list)
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
		WorkerIDs:   r.URL.Query()["worker"],
		WorkerRoles: parseWorkerRoles(r.URL.Query()),
		TraceID:     r.URL.Query().Get("trace"),
		RequestID:   r.URL.Query().Get("request"),
		Type:        event.EventType(r.URL.Query().Get("type")),
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

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
				return
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
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
		Text      string `json:"text"`
		Target    string `json:"target,omitempty"`
		InputMode string `json:"input_mode,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Attachment-bound input that no workspace worker can read gets a
	// system-reminder telling the reason worker to bridge the boundary first.
	if reasonBase.HasAttachments(body.Text) {
		if reminder := s.uploadBoundaryReminder(r.Context()); reminder != "" {
			body.Text += "\n" + reminder
		}
	}
	if err := s.hiw.SendInput(r.Context(), body.Text, body.Target, body.InputMode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// uploadBoundaryReminder returns a system-reminder for the input text when
// the upload directory falls outside every online workspace worker's mounts
// (or none is online). Empty means the boundary is fine — or the check is
// stale-cached. Best-effort: a failed bus probe counts as uncovered only
// when it indicates no workspace is reachable; failures keep the last
// cached verdict.
func (s *Server) uploadBoundaryReminder(ctx context.Context) string {
	s.coverageMu.Lock()
	cached := s.coverage
	s.coverageMu.Unlock()
	if cached != nil && time.Since(cached.at) < uploadCoverageTTL {
		if cached.covered {
			return ""
		}
		return uploadReminderText
	}

	covered := s.checkUploadCoverage(ctx)
	s.coverageMu.Lock()
	s.coverage = &uploadCoverage{at: time.Now(), covered: covered}
	s.coverageMu.Unlock()
	if covered {
		return ""
	}
	return uploadReminderText
}

// checkUploadCoverage asks every online workspace worker for its mounts and
// reports whether any mount contains the upload directory (or no workspace
// exists at all and the uploads live outside any boundary by design... which
// still counts as uncovered so the reason worker learns to bridge it).
func (s *Server) checkUploadCoverage(ctx context.Context) bool {
	dir := s.uploadDir()
	for _, id := range s.engine.OnlineWorkers() {
		if idt, ok := s.registry.Lookup(id); !ok || idt.Type != "workspace" {
			continue
		}
		reply, err := s.ask(ctx, id,
			event.New(workspaceBase.TypeMountList, "webui-hiw", nil),
			event.TypeRequestCompleted, event.TypeRequestFailed)
		if err != nil || reply.Type == event.TypeRequestFailed {
			continue // unreachable worker: not evidence of coverage
		}
		var mounts mountListResult
		parseMountResult(reply, &mounts)
		for _, m := range mounts.Mounts {
			if dir == m || strings.HasPrefix(filepath.Clean(dir)+string(filepath.Separator), filepath.Clean(m)+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// uploadReminderText tells the reason worker the uploaded attachments in this
// input sit outside its workspace boundary and how to bridge it.
const uploadReminderText = "<system-reminder>The file(s) attached to this input were uploaded to a directory that is not inside any workspace worker's mounts, so your file tools cannot read them yet. Create or reconfigure a workspace worker whose mounts include that uploads directory before reading them (spawn one through the host worker with the uploads directory and the shared programs directory as mounts).</system-reminder>"

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
	defer os.Remove(dst.Name()) // no-op after the successful rename below
	written, err := io.Copy(dst, file)
	if err != nil {
		dst.Close()
		http.Error(w, "write file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := dst.Close(); err != nil {
		http.Error(w, "close file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	final := filepath.Join(dir, filepath.Base(dst.Name()))
	if err := os.Rename(dst.Name(), final); err != nil {
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
// lifecycle state, or if external — its supervision state.
type WorkerView struct {
	ID             string                 `json:"id"`
	Type           string                 `json:"type"`
	Credential     string                 `json:"credential,omitempty"`
	PublishAllow   []event.PublishPattern `json:"publish_allow,omitempty"`
	SubscribeAllow []event.EventPattern   `json:"subscribe_allow,omitempty"`
	Online         bool                   `json:"online"`
	Managed        bool                   `json:"managed"`
	State          string                 `json:"state,omitempty"` // managed: "running" | "suspended" | "stopped" (stopped = declared but not instantiated)
	Unmanaged      bool                   `json:"unmanaged,omitempty"`
	UnmanagedState string                 `json:"unmanaged_state,omitempty"` // "running" | "stopped"
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
		for _, st := range s.unmngd.Declared() {
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
		// Only external declarations tag a worker as unmanaged: since the
		// creator also lists managed declarations, a managed worker known to
		// the registry must not pick up a stray unmanaged badge.
		if st, ok := unmngd[id.WorkerID]; ok && !st.Managed {
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

	// Include workers declared in project.json that exist nowhere else —
	// never-started externals (the UI offers a start button) and managed
	// declarations with no live worker-service entry (declared but not
	// instantiated; start spawns them via the host worker).
	for _, st := range unmngd {
		if _, ok := s.registry.Lookup(st.ID); ok {
			continue
		}
		if st.Managed {
			if _, ok := managed[st.ID]; ok {
				continue // the worker service owns it (running/suspended)
			}
			views = append(views, WorkerView{
				ID: st.ID, Type: st.Type, Managed: true, State: "stopped",
			})
			continue
		}
		views = append(views, WorkerView{
			ID: st.ID, Type: st.Type, Unmanaged: true, UnmanagedState: st.State,
		})
	}

	// The registry's List is already ID-sorted; sort the merged view too, so
	// transient managed workers land at a deterministic position instead of
	// trailing in an ever-changing insertion order.
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })

	json.NewEncoder(w).Encode(views)
}

// handleUpdateAllow edits a worker's allow lists on the bus registry. The
// request may carry either or both lists; a missing one keeps the current
// value, so callers can edit just subscribe_allow (or just publish_allow)
// without echoing the other back. SubscribeAllow patterns support the
// optional source restriction ({"type","source_id"}).
func (s *Server) handleUpdateAllow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idt, ok := s.registry.Lookup(id)
	if !ok {
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

	pubAllow := idt.PublishAllow
	if req.PublishAllow != nil {
		pubAllow = req.PublishAllow
	}
	subAllow := idt.SubscribeAllow
	if req.SubscribeAllow != nil {
		subAllow = req.SubscribeAllow
	}
	if err := s.registry.Update(id, pubAllow, subAllow); err != nil {
		http.Error(w, "update allow: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[webui] updated allow lists for %s", id)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleSuspend suspends a host-managed worker by sending the host worker's
// suspend event (data-plane, auditable).
func (s *Server) handleSuspend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	evt := event.New("suspend", "webui-hiw", map[string]any{
		"worker_id": id,
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
		"worker_id": id,
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
	if wtype != "reason" {
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

	// The reply's "result" is the JSON snapshot the worker marshaled (see
	// handleStatusQuery). Parse defensively: a shape change there must not
	// 500 the UI.
	out := providerListResult{}
	if res, _ := reply.Payload["result"].(string); res != "" {
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
	if wtype != "reason" {
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
	if res, _ := reply.Payload["result"].(string); res != "" {
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

	reply, err := s.ask(r.Context(), id,
		event.New(workspaceBase.TypeMountList, "webui-hiw", nil),
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

// handleUnmanagedStart/Stop/Restart control external (unmanaged) workers via
// the assembly-provided UnmanagedController. Start additionally covers
// declared-but-absent managed workers: a managed worker already known to the
// worker service is a 409 (suspend/resume own its lifecycle), otherwise a
// declaration in project.json is spawned via the host worker's spawn event.
func (s *Server) handleUnmanagedStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.workerSvc.HasWorker(id) {
		http.Error(w, "worker "+id+" already exists", http.StatusConflict)
		return
	}
	if s.declCreator != nil {
		payload, managed, err := s.declCreator.ManagedSpawn(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if managed {
			s.spawnManaged(w, r, payload)
			return
		}
	}
	if s.unmngd == nil {
		http.Error(w, "unmanaged control unavailable", 404)
		return
	}
	if err := s.unmngd.Start(id); err != nil {
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

// handleWorkerCreate persists a worker declaration from the UI's form body and
// launches it. External workers are started by the creator itself; managed
// workers come back with a spawn payload that is sent to the host worker as
// the auditable bus "spawn" event, and the request resolves on the host's
// request.completed / request.failed reply. A spawn failure after the
// declaration is persisted is not rolled back — the worker then shows as
// declared-but-stopped and the start endpoint retries.
func (s *Server) handleWorkerCreate(w http.ResponseWriter, r *http.Request) {
	if s.declCreator == nil {
		http.Error(w, "worker creation unavailable", 404)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	created, err := s.declCreator.Create(body)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if created.Managed {
		s.spawnManaged(w, r, created.Spawn)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(created)
}

// spawnManaged sends the host worker's spawn event with the given payload and
// waits for its terminal reply. The declaration (if any) is already persisted
// when this runs: a failure leaves the worker declared-but-stopped, retryable
// through the start endpoint.
func (s *Server) spawnManaged(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	if s.engine == nil || s.engine.Channel("host") == nil {
		http.Error(w, "host worker is offline; the declaration is persisted — press start once it is back", http.StatusServiceUnavailable)
		return
	}
	reply, err := s.ask(r.Context(), "host", event.New("spawn", "webui-hiw", payload),
		event.TypeRequestCompleted, event.TypeRequestFailed)
	if err != nil {
		// ask distinguishes timeouts / closed streams; either way the worker
		// may or may not exist now — the stopped row's start button retries.
		http.Error(w, "spawn: "+err.Error(), http.StatusGatewayTimeout)
		return
	}
	if reply.Type == event.TypeRequestFailed {
		msg, _ := reply.Payload["error"].(string)
		http.Error(w, "spawn failed: "+msg, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply.Payload)
}

// handleDeleteWorker permanently removes a worker: it stops the live process
// (managed worker / unmanaged subprocess), disconnects it from the bus, revokes
// its registry identity, deletes its persisted state, and (for unmanaged
// workers) drops its project.json declaration so it is not relaunched.
func (s *Server) handleDeleteWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Stop + clean the live side first. Managed workers live in the worker
	// service; unmanaged ones in the controller (+ project.json declaration).
	if s.workerSvc.HasWorker(id) {
		if err := s.workerSvc.DestroyWorker(id); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Also drop the project.json declaration so a managed worker is not
		// re-declared / re-built on the next project start.
		if s.declRemover != nil {
			if err := s.declRemover.RemoveDecl(id); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
	} else if s.unmngd != nil {
		if err := s.unmngd.Remove(id); err != nil {
			http.Error(w, "remove external worker: "+err.Error(), 500)
			return
		}
	}

	// Drop the live bus channel (so it cannot linger online).
	if s.engine.Channel(id) != nil {
		s.engine.Disconnect(id)
	}

	// Revoke the bus identity.
	if _, ok := s.registry.Lookup(id); ok {
		if err := s.registry.Revoke(id); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
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
	if err := s.hiw.SendEvent(r.Context(), event.EventType(body.Type), id, body.Payload); err != nil {
		http.Error(w, "send event: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleApprovals serves GET /api/approvals: the approval entries the HIW
// tracks (pending first, then the decided history). The server holds no state
// of its own — the HIW is the owner.
func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	entries := s.hiw.Approvals()
	if entries == nil {
		entries = []hiw.ApprovalEntry{}
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

	var entry hiw.ApprovalEntry
	found := false
	for _, e := range s.hiw.Approvals() {
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
	if err := s.hiw.SendApprovalDecision(r.Context(), entry.RequestID, *body.Approved, body.Note); err != nil {
		http.Error(w, "send decision: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"done": true})
}

// parseWorkerRoles reads the ?role= multi values into worker-traffic roles
// (store.RoleSent / store.RoleReceived). Unknown values are dropped; an empty
// result means both roles match.
func parseWorkerRoles(q url.Values) []string {
	var roles []string
	for _, r := range q["role"] {
		if (r == store.RoleSent || r == store.RoleReceived) && !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	return roles
}

func (s *Server) handleLoadBefore(w http.ResponseWriter, r *http.Request) {
	anchor := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	filter := eventbusapi.Filter{
		WorkerIDs:   r.URL.Query()["worker"],
		WorkerRoles: parseWorkerRoles(r.URL.Query()),
		TraceID:     r.URL.Query().Get("trace"),
		RequestID:   r.URL.Query().Get("request"),
		Type:        event.EventType(r.URL.Query().Get("type")),
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

// basicAuth protects the WebUI behind HTTP basic auth, but only for clients
// that are not on the loopback interface: same-machine access (e.g. someone
// with a shell on the box, or a local proxy) stays open, while remote peers
// must authenticate. Pure OPTIONS preflight requests are passed through so
// CORS works; the follow-up real request is still gated.
func (s *Server) basicAuth(next http.Handler) http.Handler {
	return BasicAuth(s.authUser, s.authPass, next)
}

// BasicAuth wraps a handler with HTTP basic auth that applies only to
// non-loopback clients: same-machine (loopback) access stays open, while
// remote peers must present valid credentials. With no user/pass configured
// the handler is a straight pass-through. Works for both the project WebUI and
// the control-plane SPA.
func BasicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authEnabled(user, pass) || isLoopbackRemote(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		gotUser, gotPass, ok := r.BasicAuth()
		if !ok || !secureEqual(gotUser, user) || !secureEqual(gotPass, pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="niq"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackRemote reports whether the peer address is a loopback IP
// (127.0.0.0/8 or ::1), i.e. the request originated on this machine.
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authEnabled reports whether basic auth has been configured (both a user and a
// password must be present).
func authEnabled(user, pass string) bool {
	return user != "" && pass != ""
}

// secureEqual compares two strings in constant time (via their SHA-256 digests)
// so a password guess cannot be accelerated by measuring comparison time.
func secureEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
