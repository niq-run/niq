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
	EvtExpose       event.EventType = "mcp.expose"
)

// WorkerConfig is the worker's construction config.
type WorkerConfig struct {
	ID      string
	Bus     corebus.WorkerSideChannel
	Expose  string
	Fixed   bool           // config-only: no runtime server mutation or mode switch; no snapshot
	Servers []ServerConfig // static baseline (params.servers)
	// AllowedTools, when non-empty, restricts the exposed tools to these names
	// (see Config.AllowedTools).
	AllowedTools []string

	// Params is the declaration's raw construction params, kept only so the
	// readme can be customized per declaration (params.readme / readme_append and
	// {{key}} placeholders resolved by baseworker).
	Params map[string]any

	// OnDurableChange, when set, is called after the server set changes and
	// must survive a restart, so the assembly layer can checkpoint this worker.
	// Baseworker machinery (coalesced, delivered off the event loop); nil
	// leaves signalling off.
	OnDurableChange func()
}

// Worker is the built-in "mcp" worker: agentic discovery + proxy of remote MCP.
type Worker struct {
	baseworker.BaseWorker
	cfg        WorkerConfig
	mu         sync.Mutex
	clients    map[string]*serverClient
	tools      map[string][]toolInfo        // per-server cached tool list
	toolEvents map[string][]event.EventType // ready mode: tool events bound per server
	started    bool
	cancel     context.CancelFunc
}

// New creates the mcp worker. The static baseline (params.servers) seeds
// the client set; runtime add/remove mutate it. Persistence is the host's job
// through Snapshot/Restore — the worker only signals a durable change.
func New(cfg WorkerConfig) *Worker {
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(idOrDefault(cfg.ID, "mcp"), cfg.Bus),
		cfg:        cfg,
		clients:    make(map[string]*serverClient),
		tools:      make(map[string][]toolInfo),
		toolEvents: make(map[string][]event.EventType),
	}
	w.SetOnDurableChange(cfg.OnDurableChange)
	w.seedStaticServers()
	w.registerExtensions()
	return w
}

func idOrDefault(id, def string) string {
	if id == "" {
		return def
	}
	return id
}

// seedStaticServers installs the configured baseline as the initial client set.
// Runtime changes are signalled with NotifyDurableChange and persisted by the
// host through Snapshot, so there is no separate store to keep in sync.
func (w *Worker) seedStaticServers() {
	for _, s := range w.cfg.Servers {
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

	w.RegisterReadme(
		"Read this worker's usage guide: what it proxies, the event list, and how to discover "+
			"and call remote tools. Read it first, then drive the concrete events.",
		w.readmeText, w.cfg.Params)
	w.Register(baseworker.Extension{
		Event:    EvtServerList,
		Response: baseworker.ArraySchema(baseworker.ObjectSchema(map[string]any{"name": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"}, "protocol": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"configured", "connected"}}, "tools": map[string]any{"type": "number", "description": "cached tool count"}})),
		Description: "List configured MCP servers (name, url, tool count, status). " +
			"Call this first to discover what remote MCP servers are available.",
		Parameters: obj(map[string]any{}),
	}, w.handleServerList)
	w.Register(baseworker.Extension{
		Event:       EvtToolList,
		Response:    baseworker.ArraySchema(baseworker.ObjectSchema(map[string]any{"name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "schema": map[string]any{"type": "object", "description": "the tool's input schema"}, "response": map[string]any{"type": "object", "description": "the tool's declared output schema, when the server declares one"}})),
		Description: "List the tools exposed by one MCP server (provide server).",
		Parameters: obj(map[string]any{
			"server": map[string]any{"type": "string", "description": "server name from mcp.server.list"},
		}),
	}, w.handleToolList)
	w.Register(baseworker.Extension{
		Event:       EvtToolCall,
		Response:    baseworker.StringSchema("the remote tool's content as text; when the server returns structured output, its JSON follows the text"),
		Description: "Call a tool on a remote MCP server. Provide server, tool, and arguments.",
		Parameters: obj(map[string]any{
			"server":    map[string]any{"type": "string"},
			"tool":      map[string]any{"type": "string"},
			"arguments": map[string]any{"type": "object"},
		}),
	}, w.handleToolCall)
	// Runtime mutation and mode switching are opt-out: a fixed worker is a
	// config-only tool provider (connect at start, expose tools, done).
	if !w.cfg.Fixed {
		w.Register(baseworker.Extension{
			Event:       EvtServerAdd,
			Response:    baseworker.StringSchema("confirmation naming the server"),
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
			Response:    baseworker.StringSchema("confirmation naming the server"),
			Description: "Remove an MCP server at runtime (persisted). Provide name.",
			Parameters: obj(map[string]any{
				"name": map[string]any{"type": "string"},
			}),
		}, w.handleServerRemove)
		w.Register(baseworker.Extension{
			Event:       EvtServerUpdate,
			Response:    baseworker.StringSchema("confirmation naming the server"),
			Description: "Update an MCP server's url/headers/protocol at runtime (persisted). Provide name and the fields to change.",
			Parameters: obj(map[string]any{
				"name":     map[string]any{"type": "string"},
				"url":      map[string]any{"type": "string"},
				"headers":  map[string]any{"type": "object"},
				"protocol": map[string]any{"type": "string"},
			}),
		}, w.handleServerUpdate)
		w.Register(baseworker.Extension{
			Event:    EvtExpose,
			Response: baseworker.ObjectSchema(map[string]any{"mode": map[string]any{"type": "string", "enum": []any{ExposeAgentic, ExposeReady}}}),
			Description: "Get or set the exposure mode. agentic exposes a fixed discovery surface " +
				"(server.list / tool.list / tool.call); ready additionally flattens every remote tool " +
				"into its own capability. Omit mode to read the current mode.",
			Parameters: obj(map[string]any{
				"mode": map[string]any{"type": "string", "enum": []any{ExposeAgentic, ExposeReady}},
			}),
		}, w.handleExpose)
	}

	w.registerHelp()
}

// ── ManagedWorker lifecycle ──

func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return fmt.Errorf("mcp: already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.started = true
	expose := w.cfg.Expose
	w.mu.Unlock()

	// Deliver checkpoint signals off the event loop (a no-op goroutine when the
	// assembly layer installed no callback).
	w.StartDurableLoop(runCtx)

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	// Announce outside the lock: the ready contract renders the in-memory tool
	// surface (ready mode), which reads under this same lock.
	w.AnnounceReady("mcp", nil)

	// Ready mode exposes one capability per remote tool; the lists are fetched
	// off the start path and announced once known.
	if expose == ExposeReady {
		go w.primeReadyTools(runCtx)
	}
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
// Expose rides along because the mode can change between runs: the restored
// state must carry the surface the servers were last exposed with.
type snapshotState struct {
	Servers []ServerConfig `json:"servers"`
	Expose  string         `json:"expose,omitempty"`
}

func (w *Worker) Snapshot() ([]byte, error) {
	if w.cfg.Fixed {
		// Config-authoritative: servers and expose both come from params, so
		// there is no runtime state to carry across a restart.
		return nil, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Always marshal, even when empty: an explicit empty set must be
	// distinguishable from "no snapshot", so removing every server survives a
	// restart instead of falling back to the static baseline.
	return json.Marshal(snapshotState{Servers: w.sortedServers(), Expose: w.cfg.Expose})
}

func (w *Worker) Restore(state []byte) error {
	if w.cfg.Fixed || len(state) == 0 {
		return nil
	}
	var s snapshotState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("mcp: restore: %w", err)
	}
	w.mu.Lock()
	for _, c := range w.clients {
		c.Close()
	}
	// The persisted mode is authoritative over the constructor's config, so a
	// switched mode survives the restart. Unknown/absent values keep the config.
	if validExpose(s.Expose) {
		w.cfg.Expose = s.Expose
	}
	// Withdraw the tools bound by the instance being replaced.
	var stale []event.EventType
	for _, evts := range w.toolEvents {
		stale = append(stale, evts...)
	}
	w.clients = make(map[string]*serverClient, len(s.Servers))
	w.tools = make(map[string][]toolInfo)
	w.toolEvents = make(map[string][]event.EventType)
	for _, sc := range s.Servers {
		w.clients[sc.Name] = newServerClient(sc)
	}
	w.mu.Unlock()
	if len(stale) > 0 {
		w.Unregister(stale...)
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
	w.reply(tc, list, "")
}

// mcpReadme is the agent-facing usage guide returned by worker.readme: the
// worker's "skill card". Read once, then drive the concrete events.
const mcpReadme = `# mcp — remote MCP proxy

This worker connects to remote MCP servers (Streamable HTTP) and proxies their
tools onto the bus, so you can reach external MCP tool providers.

## Workflow

1. mcp.server.list — list the configured servers (name, url, protocol, status,
   tool count).
2. mcp.tool.list { "server": "<name>" } — list one server's tools: name,
   description and JSON-schema parameters.
3. mcp.tool.call { "server": "<name>", "tool": "<tool>", "arguments": { ... } }
   — invoke a tool. The result arrives in the request.completed payload (text,
   usually JSON for structured results).

## Exposure modes

- agentic (default): only the discovery/management events are offered; discover
  tools on demand with the workflow above.
- ready: every remote tool is ALSO exposed as its own capability named
  mcp.<server>.<tool>, discovered like any other worker tool. If you see those
  flat names, call them directly with the tool's argument object (no
  server/tool wrapper).

## Runtime management (dynamic mode only)

- mcp.server.add { name, url, headers?, protocol? } — add and persist a server
  (connect + list).
- mcp.server.remove { name } — remove it, disconnect, withdraw its tools.
- mcp.server.update { name, url?, headers?, protocol? } — change it, reconnect,
  re-list.
- mcp.expose { mode? } — read (no mode) or switch ("agentic" | "ready").

Adding a server is privileged: it connects to an arbitrary URL and turns that
server's tools into callable capabilities.

## Notes

- Header values may reference ${ENV_VAR}; the project resolves them from its env.
- A call that fails on a dropped session reconnects on the next call.
- The tool surface may be narrowed by the worker's config (an allowed_tools
  allowlist); tools absent from mcp.tool.list are not callable.`

// readmeText is the guide plus a short snapshot of the current state, so an
// agent can see the mode and the available servers without an extra call.
func (w *Worker) readmeText() string {
	w.mu.Lock()
	expose := w.cfg.Expose
	fixed := w.cfg.Fixed
	allowed := append([]string(nil), w.cfg.AllowedTools...)
	type srvInfo struct {
		name  string
		tools int
	}
	servers := make([]srvInfo, 0, len(w.clients))
	for _, name := range sortedKeys(w.clients) {
		servers = append(servers, srvInfo{name: name, tools: len(w.tools[name])})
	}
	w.mu.Unlock()

	mode := "dynamic"
	if fixed {
		mode = "fixed"
	}
	var b strings.Builder
	b.WriteString(mcpReadme)
	b.WriteString("\n\n## Current\n\n")
	fmt.Fprintf(&b, "- mode: %s\n", mode)
	fmt.Fprintf(&b, "- exposure: %s\n", expose)
	if len(allowed) > 0 {
		fmt.Fprintf(&b, "- allowed tools: %s\n", strings.Join(allowed, ", "))
	} else {
		b.WriteString("- allowed tools: all\n")
	}
	if fixed {
		b.WriteString("- management: disabled (fixed mode)\n")
	} else {
		b.WriteString("- management: mcp.server.add / mcp.server.remove / mcp.server.update / mcp.expose\n")
	}
	if len(servers) == 0 {
		b.WriteString("- servers: (none configured)\n")
	} else {
		b.WriteString("- servers:\n")
		for _, s := range servers {
			fmt.Fprintf(&b, "  - %s — %d tool(s)\n", s.name, s.tools)
		}
	}
	return b.String()
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
	w.announceIfReady()
	list := make([]map[string]any, 0, len(info))
	for _, t := range info {
		entry := map[string]any{"name": t.Name, "description": t.Description, "schema": t.Schema}
		// The result shape, when the remote server declares one: the caller
		// needs it to interpret the reply (and a PTC script to parse it).
		if t.OutputSchema != nil {
			entry["response"] = t.OutputSchema
		}
		list = append(list, entry)
	}
	w.reply(tc, list, "")
}

func (w *Worker) handleToolCall(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	server := argString(tc.Args, "server")
	tool := argString(tc.Args, "tool")
	if !w.toolAllowed(tool) {
		w.reply(tc, "", fmt.Sprintf("tool %q is not allowed by config", tool))
		return
	}
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
	// A failed call drops the session, so the next call reconnects.
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

	// Prime the tool cache (and, in ready mode, the dynamic watch contract). On
	// failure, roll back (the server may be offline) and surface the error.
	if _, err := w.toolsFor(context.Background(), sc.Name); err != nil {
		w.mu.Lock()
		delete(w.clients, sc.Name)
		cl.Close()
		w.mu.Unlock()
		w.reply(tc, "", err.Error())
		return
	}
	w.announceIfReady()
	w.NotifyDurableChange()
	w.reply(tc, fmt.Sprintf("added server %s (%d tools)", sc.Name, w.toolsForCached(sc.Name)), "")
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
		w.setTools(name, nil)
	}
	w.mu.Unlock()
	if !ok {
		w.reply(tc, "", fmt.Sprintf("unknown server %q", name))
		return
	}
	w.NotifyDurableChange()
	w.announceIfReady()
	w.reply(tc, fmt.Sprintf("removed server %s", name), "")
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
	w.setTools(name, nil) // stale until re-listed against the new endpoint
	w.mu.Unlock()

	w.NotifyDurableChange()
	// Re-list against the new endpoint off the reply path, then re-announce;
	// best-effort — an unreachable server simply has no tools until it recovers.
	go func() {
		if _, err := w.toolsFor(context.Background(), name); err != nil {
			log.Printf("[mcp %s] refresh tools for %s: %v", w.ID(), name, err)
		}
		w.announceIfReady()
	}()
	w.reply(tc, fmt.Sprintf("updated server %s", name), "")
}

func (w *Worker) handleExpose(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	mode := argString(tc.Args, "mode")
	if mode == "" {
		w.mu.Lock()
		cur := w.cfg.Expose
		w.mu.Unlock()
		w.reply(tc, map[string]any{"mode": cur}, "")
		return
	}
	if !validExpose(mode) {
		w.reply(tc, "", fmt.Sprintf("unknown expose mode %q (want %s or %s)", mode, ExposeAgentic, ExposeReady))
		return
	}
	w.applyExpose(mode)
	w.reply(tc, map[string]any{"mode": mode}, "")
}

// applyExpose switches the exposed capability surface and makes the change
// durable. Withdrawing ready-mode tools happens under the lock; registering
// them again requires re-listing, so it is primed off the event path.
func (w *Worker) applyExpose(mode string) {
	w.mu.Lock()
	changed := w.cfg.Expose != mode
	w.cfg.Expose = mode
	if changed && mode == ExposeAgentic {
		w.withdrawToolsLocked()
	}
	started := w.started
	w.mu.Unlock()
	if !changed {
		return
	}

	w.NotifyDurableChange()
	w.announce()
	if mode == ExposeReady && started {
		go w.primeReadyTools(context.Background())
	}
}

// withdrawToolsLocked unregisters every ready-mode tool extension, leaving the
// cached tool lists in place for a later re-registration. Caller holds w.mu.
func (w *Worker) withdrawToolsLocked() {
	for server := range w.toolEvents {
		w.unregisterToolEvents(server)
	}
}

// announce re-broadcasts the current contract, when started. Unlike
// announceIfReady it does not gate on the mode, so switching to agentic can
// make peers drop the withdrawn tools.
func (w *Worker) announce() {
	w.mu.Lock()
	started := w.started
	w.mu.Unlock()
	if started {
		w.AnnounceReady("mcp", nil)
	}
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
	info = w.filterAllowed(info)
	w.mu.Lock()
	// The server may have been removed while we were listing; never resurrect
	// tools for a server that is no longer present.
	if _, ok := w.clients[server]; !ok {
		w.mu.Unlock()
		return nil, fmt.Errorf("server %q removed", server)
	}
	w.setTools(server, info)
	w.mu.Unlock()
	return info, nil
}

func (w *Worker) toolsForCached(server string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.tools[server])
}

// filterAllowed drops tools not in the configured allowlist (all pass when it
// is unset). Applied to the freshly listed tools so the cache — and thus the
// ready surface and mcp.tool.list — only ever holds allowed tools.
func (w *Worker) filterAllowed(info []toolInfo) []toolInfo {
	if len(w.cfg.AllowedTools) == 0 {
		return info
	}
	allow := make(map[string]bool, len(w.cfg.AllowedTools))
	for _, n := range w.cfg.AllowedTools {
		allow[n] = true
	}
	out := make([]toolInfo, 0, len(info))
	for _, t := range info {
		if allow[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// toolAllowed reports whether a tool name is callable under the allowlist.
func (w *Worker) toolAllowed(name string) bool {
	if len(w.cfg.AllowedTools) == 0 {
		return true
	}
	for _, n := range w.cfg.AllowedTools {
		if n == name {
			return true
		}
	}
	return false
}

// setTools caches a server's tool list and, in ready mode, binds one extension
// per tool (mcp.<server>.<tool>), replacing whatever was bound before. A server
// with no tools has its extensions withdrawn. Caller holds w.mu.
func (w *Worker) setTools(server string, info []toolInfo) {
	w.unregisterToolEvents(server)
	if len(info) == 0 {
		delete(w.tools, server)
		return
	}
	w.tools[server] = info
	if w.cfg.Expose != ExposeReady {
		return
	}
	events := make([]event.EventType, 0, len(info))
	for _, t := range info {
		evt := toolEventName(server, t.Name)
		if reservedEvent(evt) {
			log.Printf("[mcp %s] tool %s/%s not exposed: event %s is reserved", w.ID(), server, t.Name, evt)
			continue
		}
		serverName, toolName := server, t.Name
		ext := baseworker.Extension{
			Event:       evt,
			Description: firstNonEmpty(t.Description, t.Name),
			Parameters:  mapToStringSchema(t.Schema),
		}
		if t.OutputSchema != nil {
			// Republish the remote tool's declared result shape as the capability's
			// response half, so peers (and the directory roster) can see it before
			// calling.
			ext.Response = mapToStringSchema(t.OutputSchema)
		}
		w.Register(ext, func(e event.Event) { w.handleDirectCall(e, serverName, toolName) })
		events = append(events, evt)
	}
	w.toolEvents[server] = events
}

// unregisterToolEvents withdraws the tool extensions bound to one server.
// Caller holds w.mu.
func (w *Worker) unregisterToolEvents(server string) {
	if evts := w.toolEvents[server]; len(evts) > 0 {
		w.Unregister(evts...)
	}
	delete(w.toolEvents, server)
}

// reservedEvent reports whether an event type belongs to the fixed management
// group. Register replaces by event type, so a remote tool must never be
// allowed to overwrite one.
func reservedEvent(evt event.EventType) bool {
	switch evt {
	case EvtServerList, EvtServerAdd, EvtServerRemove, EvtServerUpdate, EvtToolList, EvtToolCall, EvtExpose, event.TypeWorkerReadme, event.TypeWorkerHelp:
		return true
	}
	return false
}

// toolEventName is the ready-mode event type for one remote tool.
func toolEventName(server, tool string) event.EventType {
	return event.EventType("mcp." + sanitizeEvent(server) + "." + sanitizeEvent(tool))
}

// announceIfReady re-broadcasts the ready contract after the in-memory tool
// surface changed. Only ready mode's contract is data-driven; agentic mode
// advertises a fixed set, so there is nothing to update.
func (w *Worker) announceIfReady() {
	w.mu.Lock()
	expose := w.cfg.Expose
	started := w.started
	w.mu.Unlock()
	if expose == ExposeReady && started {
		w.AnnounceReady("mcp", nil)
	}
}

// primeReadyTools fetches every server's tool list so the ready contract
// carries the per-tool surface, then announces. Runs off the start path because
// listing is a network call.
func (w *Worker) primeReadyTools(ctx context.Context) {
	w.mu.Lock()
	names := sortedKeys(w.clients)
	w.mu.Unlock()
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		if _, err := w.toolsFor(ctx, name); err != nil {
			log.Printf("[mcp %s] prime %s: %v", w.ID(), name, err)
		}
	}
	w.announceIfReady()
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

func (w *Worker) sortedServers() []ServerConfig {
	names := sortedKeys(w.clients)
	out := make([]ServerConfig, 0, len(names))
	for _, n := range names {
		out = append(out, w.clients[n].cfg)
	}
	return out
}

// reply answers a tool call: result is a value (a list of servers/tools, an
// object, or text) and ReplyCompleted decides how it travels.
func (w *Worker) reply(tc baseworker.ToolCall, result any, errStr string) {
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
