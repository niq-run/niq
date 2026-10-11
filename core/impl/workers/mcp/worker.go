package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/niq-run/niq/core/impl/baseworker"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// Event types served by the mcp worker (agentic discovery + runtime management).
const (
	EvtServerList   event.EventType = "mcp.server.list"
	EvtServerAdd    event.EventType = "mcp.server.add"
	EvtServerRemove event.EventType = "mcp.server.remove"
	EvtServerUpdate event.EventType = "mcp.server.update"
	EvtToolList     event.EventType = "mcp.tool.list"
	EvtToolCall     event.EventType = "mcp.tool.call"
)

// WorkerConfig is the worker's construction config.
type WorkerConfig struct {
	ID      string
	Bus     corebus.WorkerSideChannel
	Expose  string
	Servers []ServerConfig // static baseline (params.mcp.servers)
	Store   ServerStore    // persistence for the dynamic set; may be nil
}

// Worker is the built-in "mcp" worker: agentic discovery + proxy of remote MCP.
type Worker struct {
	baseworker.BaseWorker
	cfg     WorkerConfig
	store   ServerStore
	mu      sync.Mutex
	clients map[string]*serverClient
	tools   map[string][]toolInfo // per-server cached tool list
	started bool
	cancel  context.CancelFunc
}

// New creates the mcp worker. The effective server set is the persisted store
// (authoritative once written) else the static baseline; the baseline is
// seeded to the store on first start so dynamic adds layer on top.
func New(cfg WorkerConfig) *Worker {
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(idOrDefault(cfg.ID, "mcp"), cfg.Bus),
		cfg:        cfg,
		store:      cfg.Store,
		clients:    make(map[string]*serverClient),
		tools:      make(map[string][]toolInfo),
	}
	w.loadEffectiveServers()
	w.registerExtensions()
	return w
}

func idOrDefault(id, def string) string {
	if id == "" {
		return def
	}
	return id
}

// loadEffectiveServers resolves the authoritative set (store else baseline)
// and constructs the per-server clients.
func (w *Worker) loadEffectiveServers() {
	servers := w.cfg.Servers
	if w.store != nil {
		if stored, err := w.store.Load(); err == nil && len(stored) > 0 {
			servers = stored
		} else if len(servers) > 0 {
			// First run: seed the store so later runtime adds layer on it.
			_ = w.store.Save(servers)
		}
	}
	for _, s := range servers {
		w.clients[s.Name] = newServerClient(s)
	}
}

func (w *Worker) registerExtensions() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	str := func(desc string, required bool) map[string]any {
		m := map[string]any{"type": "string", "description": desc}
		return m
	}
	_ = str

	w.Register(baseworker.Extension{
		Event: EvtServerList,
		Description: "List configured MCP servers (name, url, tool count, status). " +
			"Call this first to discover what remote MCP servers are available.",
		Parameters: obj(map[string]any{}),
	}, w.handleServerList)
	w.Register(baseworker.Extension{
		Event:       EvtToolList,
		Description: "List the tools exposed by one MCP server (provide server).",
		Parameters: obj(map[string]any{
			"server": map[string]any{"type": "string", "description": "server name from mcp.server.list"},
		}),
	}, w.handleToolList)
	w.Register(baseworker.Extension{
		Event:       EvtToolCall,
		Description: "Call a tool on a remote MCP server. Provide server, tool, and arguments.",
		Parameters: obj(map[string]any{
			"server":    map[string]any{"type": "string"},
			"tool":      map[string]any{"type": "string"},
			"arguments": map[string]any{"type": "object"},
		}),
	}, w.handleToolCall)
	w.Register(baseworker.Extension{
		Event:       EvtServerAdd,
		Description: "Add an MCP server at runtime (persisted). Provide name, url, and optional headers/protocol.",
		Parameters: obj(map[string]any{
			"name":     map[string]any{"type": "string"},
			"url":      map[string]any{"type": "string"},
			"headers":  map[string]any{"type": "object"},
			"protocol": map[string]any{"type": "string"},
		}),
	}, w.handleServerAdd)
	w.Register(baseworker.Extension{
		Event:       EvtServerRemove,
		Description: "Remove an MCP server at runtime (persisted). Provide name.",
		Parameters: obj(map[string]any{
			"name": map[string]any{"type": "string"},
		}),
	}, w.handleServerRemove)
	w.Register(baseworker.Extension{
		Event:       EvtServerUpdate,
		Description: "Update an MCP server's url/headers/protocol at runtime (persisted). Provide name and the fields to change.",
		Parameters: obj(map[string]any{
			"name":     map[string]any{"type": "string"},
			"url":      map[string]any{"type": "string"},
			"headers":  map[string]any{"type": "object"},
			"protocol": map[string]any{"type": "string"},
		}),
	}, w.handleServerUpdate)
}

// ── ManagedWorker lifecycle ──

func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("mcp: already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.AnnounceReady("mcp", nil)
	w.started = true
	return nil
}

func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	for _, c := range w.clients {
		c.Close()
	}
	if w.cancel != nil {
		w.cancel()
	}
	w.started = false
	return nil
}

// snapshotState is the persisted form carried by workerhost snapshot/restore.
type snapshotState struct {
	Servers []ServerConfig `json:"servers"`
}

func (w *Worker) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	servers := w.sortedServers()
	if len(servers) == 0 {
		return nil, nil
	}
	return json.Marshal(snapshotState{Servers: servers})
}

func (w *Worker) Restore(state []byte) error {
	if len(state) == 0 {
		return nil
	}
	var s snapshotState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("mcp: restore: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.clients {
		c.Close()
	}
	w.clients = make(map[string]*serverClient, len(s.Servers))
	w.tools = make(map[string][]toolInfo)
	for _, sc := range s.Servers {
		w.clients[sc.Name] = newServerClient(sc)
	}
	return nil
}

func (w *Worker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt, ok := <-busCh:
			if !ok {
				return
			}
			w.process(evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) process(evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerDiscover:
		if evt.WorkerId != w.ID() {
			w.AnnounceReadyTo(evt.WorkerId, "mcp", nil, false)
		}
	default:
		if !w.DispatchExtension(evt) {
			log.Printf("[mcp %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}

// ── agentic discovery handlers ──

func (w *Worker) handleServerList(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	list := make([]map[string]any, 0, len(w.clients))
	for _, name := range sortedKeys(w.clients) {
		sc := w.clients[name].cfg
		status := "configured"
		if w.clients[name].session != nil {
			status = "connected"
		}
		list = append(list, map[string]any{
			"name":     sc.Name,
			"url":      sc.URL,
			"protocol": firstNonEmpty(sc.Protocol, "streamable-http"),
			"status":   status,
			"tools":    len(w.tools[sc.Name]),
		})
	}
	b, _ := json.Marshal(list)
	w.reply(tc, string(b), "")
}

func (w *Worker) handleToolList(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	server := argString(tc.Args, "server")
	info, err := w.toolsFor(context.Background(), server)
	if err != nil {
		w.reply(tc, "", err.Error())
		return
	}
	list := make([]map[string]any, 0, len(info))
	for _, t := range info {
		list = append(list, map[string]any{"name": t.Name, "description": t.Description, "schema": t.Schema})
	}
	b, _ := json.Marshal(list)
	w.reply(tc, string(b), "")
}

func (w *Worker) handleToolCall(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	server := argString(tc.Args, "server")
	tool := argString(tc.Args, "tool")
	args, _ := tc.Args["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}

	w.mu.Lock()
	cl, ok := w.clients[server]
	w.mu.Unlock()
	if !ok {
		w.reply(tc, "", fmt.Sprintf("unknown server %q", server))
		return
	}
	// Reconnect once on transient session failure.
	result, err := cl.CallTool(context.Background(), tool, args)
	if err != nil {
		w.reply(tc, "", err.Error())
		return
	}
	w.reply(tc, result, "")
}

// ── runtime management handlers (persisted) ──

func (w *Worker) handleServerAdd(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	sc, perr := serverFromArgs(tc.Args)
	if perr != nil {
		w.reply(tc, "", perr.Error())
		return
	}

	w.mu.Lock()
	if _, exists := w.clients[sc.Name]; exists {
		w.mu.Unlock()
		w.reply(tc, "", fmt.Sprintf("server %q already exists", sc.Name))
		return
	}
	cl := newServerClient(sc)
	w.clients[sc.Name] = cl
	w.mu.Unlock()

	// Prime tools + (in ready mode) register per-tool extensions. On failure,
	// roll back (the server may be offline) and surface the error.
	if _, err := w.toolsFor(context.Background(), sc.Name); err != nil {
		w.mu.Lock()
		delete(w.clients, sc.Name)
		cl.Close()
		w.mu.Unlock()
		w.reply(tc, "", err.Error())
		return
	}
	w.persist()
	w.reply(tc, "", fmt.Sprintf("added server %s (%d tools)", sc.Name, w.toolsForCached(sc.Name)))
}

func (w *Worker) handleServerRemove(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	name := argString(tc.Args, "name")
	w.mu.Lock()
	cl, ok := w.clients[name]
	if ok {
		cl.Close()
		delete(w.clients, name)
		delete(w.tools, name)
	}
	w.mu.Unlock()
	if !ok {
		w.reply(tc, "", fmt.Sprintf("unknown server %q", name))
		return
	}
	w.persist()
	w.reply(tc, "", fmt.Sprintf("removed server %s", name))
}

func (w *Worker) handleServerUpdate(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	name := argString(tc.Args, "name")

	w.mu.Lock()
	cl, ok := w.clients[name]
	if !ok {
		w.mu.Unlock()
		w.reply(tc, "", fmt.Sprintf("unknown server %q", name))
		return
	}
	sc := cl.cfg
	if u := argString(tc.Args, "url"); u != "" {
		sc.URL = u
	}
	if p := argString(tc.Args, "protocol"); p != "" {
		sc.Protocol = p
	}
	if hm, ok := tc.Args["headers"].(map[string]any); ok {
		sc.Headers = stringMap(hm)
	}
	cl.Close()
	next := newServerClient(sc)
	w.clients[name] = next
	w.mu.Unlock()

	w.persist()
	w.reply(tc, "", fmt.Sprintf("updated server %s", name))
}

// ── helpers ──

// toolsFor returns the tool list for a server, connecting + listing + (re)caching.
func (w *Worker) toolsFor(ctx context.Context, server string) ([]toolInfo, error) {
	w.mu.Lock()
	cl, ok := w.clients[server]
	w.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown server %q", server)
	}
	info, err := cl.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.tools[server] = info
	w.mu.Unlock()
	if w.cfg.Expose == ExposeReady && w.started {
		w.registerServerTools(server, info)
	}
	return info, nil
}

func (w *Worker) toolsForCached(server string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.tools[server])
}

// registerServerTools registers one peer-callable extension per remote tool
// (ready mode): mcp.<server>.<tool> → that tool, so reason discovers them flat.
func (w *Worker) registerServerTools(server string, tools []toolInfo) {
	for _, t := range tools {
		evtType := event.EventType("mcp." + sanitizeEvent(server) + "." + sanitizeEvent(t.Name))
		serverName, toolName := server, t.Name
		w.Register(baseworker.Extension{
			Event:       evtType,
			Description: firstNonEmpty(t.Description, t.Name),
			Parameters:  mapToStringSchema(t.Schema),
		}, func(e event.Event) { w.handleDirectCall(e, serverName, toolName) })
	}
}

func (w *Worker) handleDirectCall(evt event.Event, server, tool string) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	args := evt.Payload
	if args == nil {
		args = map[string]any{}
	}
	w.mu.Lock()
	cl, ok := w.clients[server]
	w.mu.Unlock()
	if !ok {
		w.reply(tc, "", fmt.Sprintf("unknown server %q", server))
		return
	}
	result, err := cl.CallTool(context.Background(), tool, args)
	if err != nil {
		w.reply(tc, "", err.Error())
		return
	}
	w.reply(tc, result, "")
}

func (w *Worker) persist() {
	if w.store == nil {
		return
	}
	w.mu.Lock()
	servers := w.sortedServers()
	w.mu.Unlock()
	if err := w.store.Save(servers); err != nil {
		log.Printf("[mcp %s] persist servers: %v", w.ID(), err)
	}
}

func (w *Worker) sortedServers() []ServerConfig {
	names := sortedKeys(w.clients)
	out := make([]ServerConfig, 0, len(names))
	for _, n := range names {
		out = append(out, w.clients[n].cfg)
	}
	return out
}

func (w *Worker) reply(tc baseworker.ToolCall, result, errStr string) {
	if errStr != "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, errStr, tc.TraceID)
		return
	}
	w.ReplyCompleted(tc.CallerID, tc.CallID, result, tc.TraceID)
}

func serverFromArgs(args map[string]any) (ServerConfig, error) {
	sc := ServerConfig{}
	sc.Name = argString(args, "name")
	sc.URL = argString(args, "url")
	sc.Protocol = argString(args, "protocol")
	if hm, ok := args["headers"].(map[string]any); ok {
		sc.Headers = stringMap(hm)
	}
	if sc.Name == "" || sc.URL == "" {
		return sc, fmt.Errorf("server add requires name and url")
	}
	return sc, nil
}

func stringMap(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func argString(args map[string]any, key string) string {
	if s, ok := args[key].(string); ok {
		return s
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sanitizeEvent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "tool"
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func mapToStringSchema(s any) map[string]any {
	if s == nil {
		return map[string]any{"type": "object"}
	}
	if m, ok := s.(map[string]any); ok {
		return m
	}
	return map[string]any{"type": "object", "schema": s}
}