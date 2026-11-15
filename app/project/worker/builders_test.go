package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	providerpkg "github.com/niq-run/niq/app/project/provider"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/iface"
	"github.com/niq-run/niq/core/impl/workers/hiw"
	"github.com/niq-run/niq/core/impl/workersvc"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

func TestParseMountsParamTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	p := map[string]any{
		"mounts": []any{
			"~/.niq/projects/alpha/workspace",
			"~/uploads",
			"/abs/path",
			"~",
		},
	}
	mounts, err := parseMountsParam(p)
	if err != nil {
		t.Fatalf("parseMountsParam: %v", err)
	}
	want := []string{
		filepath.Join(home, ".niq", "projects", "alpha", "workspace"),
		filepath.Join(home, "uploads"),
		"/abs/path",
		home,
	}
	if len(mounts) != len(want) {
		t.Fatalf("got %d mounts, want %d: %v", len(mounts), len(want), mounts)
	}
	for i, m := range mounts {
		if m != want[i] {
			t.Errorf("mount[%d] = %q, want %q", i, m, want[i])
		}
	}
}

func TestParseMountsParamPathSugar(t *testing.T) {
	p := map[string]any{"path": "~/.niq/projects/beta/programs"}
	mounts, err := parseMountsParam(p)
	if err != nil {
		t.Fatalf("parseMountsParam: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".niq", "projects", "beta", "programs")
	if len(mounts) != 1 || mounts[0] != want {
		t.Fatalf("mounts = %v, want [%s]", mounts, want)
	}
}

// TestBuildNIWSpecRequiresRemoteParams verifies a NIW without the required
// params is rejected before any registry/listener is touched — so a zero
// BuildContext is safe here.
func TestBuildNIWSpecRequiresRemoteParams(t *testing.T) {
	var empty BuildContext

	if _, err := buildNIQSpec(empty, coreworker.WorkerConfig{ID: "peer"}); err == nil {
		t.Fatal("expected error for missing remote params, got nil")
	}

	// remote_worker_id alone (no URL) is still invalid.
	if _, err := buildNIQSpec(empty, coreworker.WorkerConfig{
		ID:     "peer",
		Params: map[string]any{"remote_worker_id": "far"},
	}); err == nil {
		t.Fatal("expected error for missing remote_url")
	}

	// A recipient is NOT required at build time: it is only the seed — the live
	// binding is a runtime property set via niq.recipient.* (like lark).
	if _, err := buildNIQSpec(empty, coreworker.WorkerConfig{
		ID: "peer",
		Params: map[string]any{
			"remote_url":        "http://127.0.0.1:19763",
			"remote_worker_id":  "from-b",
			"remote_credential": "tok",
		},
	}); err != nil {
		t.Fatalf("recipient should be optional (runtime/settable), got %v", err)
	}
}

// TestBuildNIQSpecReadsParams verifies the niq spec picks up remote_url,
// remote_worker_id and recipient from Params (validated before connectivity, so
// a zero BuildContext is fine — specConnect is not reached past this check).
func TestBuildNIQSpecReadsParams(t *testing.T) {
	var empty BuildContext
	spec, err := buildNIQSpec(empty, coreworker.WorkerConfig{
		ID: "peer",
		Params: map[string]any{
			"remote_url":       "http://127.0.0.1:19763",
			"remote_worker_id": "from-b",
			"recipient":        "reason.local",
		},
	})
	if err != nil {
		t.Fatalf("buildNIQSpec: %v", err)
	}
	if spec.Type() != "niq" || spec.ID() != "peer" {
		t.Fatalf("unexpected spec: type=%s id=%s", spec.Type(), spec.ID())
	}
	// The Build closure carries the config; invoke it with a nil channel to
	// confirm it constructs an interface worker over the niq backend.
	if w := spec.Build(nil); w == nil {
		t.Fatal("Build returned nil worker")
	} else if _, ok := w.(*iface.Worker); !ok {
		t.Fatalf("build returned %T, want *iface.Worker", w)
	}
}

// TestSpawnParamsCoverEveryBuilder is the invariant behind per-type validation:
// a type the service can build must also declare what its spawn accepts,
// otherwise a new builder would quietly ship without a check.
func TestSpawnParamsCoverEveryBuilder(t *testing.T) {
	svc := workersvc.New()
	RegisterBuilders(BuildContext{}, svc)

	for _, typ := range svc.RegisteredTypes() {
		if _, ok := SpawnParamsByType[typ]; !ok {
			t.Errorf("builder type %q has no spawn-parameter contract in SpawnParamsByType", typ)
		}
	}
	for typ := range SpawnParamsByType {
		if !slices.Contains(svc.RegisteredTypes(), typ) {
			t.Errorf("SpawnParamsByType declares %q, but no builder is registered for it", typ)
		}
	}
}

// TestSpawnRejectsUnknownParamsEndToEnd drives the real registration path: the
// host's CreateWorker call must fail on the check and never reach the builder.
func TestSpawnRejectsUnknownParamsEndToEnd(t *testing.T) {
	svc := workersvc.New()
	RegisterBuilders(BuildContext{}, svc)

	_, err := svc.CreateWorker(context.Background(), coreworker.WorkerConfig{
		ID: "analyst", Type: "reason",
		Params: map[string]any{"type": "reason", "id": "analyst", "prompt": "Triage issues."},
	})
	if err == nil {
		t.Fatal("CreateWorker accepted an unread spawn parameter")
	}
	if !strings.Contains(err.Error(), `"prompt"`) || !strings.Contains(err.Error(), "reason accepts") {
		t.Fatalf("err = %q, want the unknown-parameter error with the accepted list", err)
	}
	if svc.HasWorker("analyst") {
		t.Fatal("the rejected spawn still registered a worker")
	}
}

// ── webui-hiw construction (buildHIWSpec) ──
//
// These cover the CONTRACT a running project must expose, not the hiw
// implementation (which lives in core/impl/workers/hiw).

// TestHIWAnnouncesApprovalCapability verifies the contract a running project
// must expose: webui-hiw may hear the fleet's discover broadcast, and the watch
// it announces carries approval.request with an argument schema — otherwise
// the approval flow is invisible to the directory roster and to any worker
// resolving "who approves boundary expansions".
func TestHIWAnnouncesApprovalCapability(t *testing.T) {
	bc, svc := newTestEngine(t)
	ctx := context.Background()

	err := svc.RecoverAll(ctx, workersvc.RecoverOptions{
		Essential: []coreworker.WorkerConfig{{ID: DefaultWebUIHIWID, Type: "hiw"}},
	})
	if err != nil {
		t.Fatalf("recover hiw: %v", err)
	}

	id, ok := bc.Registry.Lookup(DefaultWebUIHIWID)
	if !ok {
		t.Fatal("hiw identity not registered")
	}
	var discovers bool
	for _, p := range id.SubscribeAllow {
		if p.Type == event.TypeWorkerDiscover {
			discovers = true
		}
	}
	if !discovers {
		t.Fatalf("hiw cannot hear discover broadcasts: subscribe=%+v", id.SubscribeAllow)
	}

	h, ok := svc.Worker(DefaultWebUIHIWID)
	if !ok {
		t.Fatal("hiw worker not running")
	}
	hw, ok := h.(*hiw.Worker)
	if !ok {
		t.Fatalf("hiw worker has type %T", h)
	}
	var advertised bool
	for _, e := range hw.WatchEntries(false) {
		if e["event"] != string(event.TypeApprovalRequest) {
			continue
		}
		advertised = true
		if _, ok := e["parameters"]; !ok {
			t.Fatalf("approval.request advertised without a schema: %+v", e)
		}
	}
	if !advertised {
		t.Fatalf("approval.request missing from the watch contract: %+v", hw.WatchEntries(false))
	}
}

// TestHIWIdentityRefreshesOnRestart covers the upgrade path: a project whose
// persisted webui-hiw identity predates the discover grant (subscribe empty)
// must have it refreshed when the worker connects, not keep the old contract.
func TestHIWIdentityRefreshesOnRestart(t *testing.T) {
	dir := t.TempDir()
	reg, err := eventbus.NewFileIdentityRegistry(filepath.Join(dir, "id", "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	stale := corebus.Identity{
		WorkerID:     DefaultWebUIHIWID,
		Type:         "hiw",
		PublishAllow: []event.PublishPattern{event.NewPublishPattern("*")},
	}
	if err := reg.Register(stale); err != nil {
		t.Fatalf("seed stale identity: %v", err)
	}

	bc, svc := newTestEngineWith(t, dir, reg)
	err = svc.RecoverAll(context.Background(), workersvc.RecoverOptions{
		Essential: []coreworker.WorkerConfig{{ID: DefaultWebUIHIWID, Type: "hiw"}},
	})
	if err != nil {
		t.Fatalf("recover hiw: %v", err)
	}

	id, ok := bc.Registry.Lookup(DefaultWebUIHIWID)
	if !ok {
		t.Fatal("hiw identity not registered")
	}
	var discovers bool
	for _, p := range id.SubscribeAllow {
		if p.Type == event.TypeWorkerDiscover {
			discovers = true
		}
	}
	if !discovers {
		t.Fatalf("stale identity was not refreshed: subscribe=%+v", id.SubscribeAllow)
	}
}

// ── harness: a project assembly without the network ──
//
// newTestEngine wires up a registry, engine and in-process listener exactly as
// runAssembly does, returning the build context, worker service and the store dir.
func newTestEngine(t *testing.T) (BuildContext, *workersvc.WorkerService) {
	t.Helper()
	dir := t.TempDir()

	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(dir, "id", "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return newTestEngineWith(t, dir, registry)
}

// newTestEngineWith is newTestEngine over a caller-supplied registry, so a test
// can pre-seed persisted identities (an upgrade from an older contract).
func newTestEngineWith(t *testing.T, dir string, registry corebus.IdentityRegistry) (BuildContext, *workersvc.WorkerService) {
	t.Helper()

	engine := eventbus.NewEngine(registry, eventbus.NewMemoryEventStore())
	listener := inprocess.NewInProcListener()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		for {
			ch, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			eventbus.Attach(ctx, engine, ch.WorkerID(), ch)
		}
	}()

	svc := workersvc.New()
	bc := BuildContext{
		Registry:  registry,
		Listener:  listener,
		Engine:    engine,
		WorkerSvc: svc,
	}
	RegisterBuilders(bc, svc)
	return bc, svc
}

// ── lifecycle over the real builders ──
//
// build.go constructs a worker; these drive what it then does through the
// worker service: suspend/resume, recovery from the state store, and the ACL
// grants its identity carries.

func TestWorkspaceSuspendResume(t *testing.T) {
	_, svc := newTestEngine(t)
	ctx := context.Background()

	if _, err := svc.CreateWorker(ctx, coreworker.WorkerConfig{
		Type: "workspace", Params: map[string]any{"mounts": []string{t.TempDir()}},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	wsID := firstWorkerOfType(svc, "workspace")
	if wsID == "" {
		t.Fatal("no workspace worker created")
	}

	if info := workerStatus(svc, wsID); info.State != coreworker.StateRunning {
		t.Fatalf("expected running, got %s", info.State)
	}

	if err := svc.SuspendWorker(wsID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if info := workerStatus(svc, wsID); info.State != coreworker.StateSuspended {
		t.Fatalf("expected suspended, got %s", info.State)
	}

	if err := svc.ResumeWorker(ctx, wsID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if info := workerStatus(svc, wsID); info.State != coreworker.StateRunning {
		t.Fatalf("expected running after resume, got %s", info.State)
	}
}

func firstWorkerOfType(svc *workersvc.WorkerService, typ string) string {
	for _, wi := range svc.ListWorkers("") {
		if wi.Type == typ {
			return wi.ID
		}
	}
	return ""
}

func workerStatus(svc *workersvc.WorkerService, id string) workersvc.WorkerInfo {
	for _, wi := range svc.ListWorkers("") {
		if wi.ID == id {
			return wi
		}
	}
	return workersvc.WorkerInfo{}
}

// TestReasonFamilyRejectsUnknownProvider verifies a reason worker must name a
// provider that exists in provider.json: there is no ad-hoc endpoint/credential
// fallback any more, so a typo fails the spawn with a clear message instead of
// silently building a provider nobody configured.
func TestReasonFamilyRejectsUnknownProvider(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", cfgPath)
	if err := providerpkg.Write(&providerpkg.Config{Providers: []providerpkg.Entry{
		{Name: "known", Type: "deepseek", APIKey: "k", Model: "m1"},
	}}); err != nil {
		t.Fatal(err)
	}

	_, err := resolveReasonFamily(coreworker.WorkerConfig{
		ID:     "r1",
		Params: map[string]any{"provider": "typo-name", "model": "m1"},
	}, "reason")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unknown provider: err = %v, want a 'not configured' error", err)
	}

	in, err := resolveReasonFamily(coreworker.WorkerConfig{
		ID:     "r2",
		Params: map[string]any{"provider": "known", "model": "m1"},
	}, "reason")
	if err != nil {
		t.Fatalf("known provider rejected: %v", err)
	}
	if in.pname != "known" || in.pmodel != "m1" {
		t.Fatalf("resolved %s/%s, want known/m1", in.pname, in.pmodel)
	}
}

// TestSubscriptionSpecParsing verifies a subscription entry accepts both the
// bare type string and the {"type","source"} object form.
func TestSubscriptionSpecParsing(t *testing.T) {
	raw := `{"workers":[{"type":"reason","id":"r","subscriptions":[
		"worker.discover",
		{"type":"request.completed","source":"timer"}
	]}]}`
	ws := parseDeclarations(t, raw)
	subs := ws[0].Subscriptions
	if len(subs) != 2 {
		t.Fatalf("subscriptions = %+v, want 2 entries", subs)
	}
	if subs[0].Type != "worker.discover" || subs[0].Source != "" {
		t.Fatalf("string entry = %+v, want type-only worker.discover", subs[0])
	}
	if subs[1].Type != "request.completed" || subs[1].Source != "timer" {
		t.Fatalf("object entry = %+v, want request.completed from timer", subs[1])
	}

	// The declaration's subscription specs lower to the identity ACL.
	patterns := SpawnConfig(ws[0]).SubscribeAllow
	if len(patterns) != 2 {
		t.Fatalf("patterns = %+v, want 2", patterns)
	}
	if patterns[0].Type != "worker.discover" || !patterns[0].UnrestrictedSource() {
		t.Fatalf("pattern[0] = %+v, want worker.discover from any source", patterns[0])
	}
	if patterns[0].SourceID != "*" {
		t.Fatalf("pattern[0].SourceID = %q, want the canonical %q", patterns[0].SourceID, "*")
	}
	if patterns[1].Type != "request.completed" || patterns[1].SourceID != "timer" {
		t.Fatalf("pattern[1] = %+v, want request.completed/timer", patterns[1])
	}
	if !patterns[1].Matches(event.New("request.completed", "timer", nil)) {
		t.Fatal("pattern[1] should match request.completed from timer")
	}
	if patterns[1].Matches(event.New("request.completed", "niq", nil)) {
		t.Fatal("pattern[1] must reject request.completed from another source")
	}
}

// TestSubAllowFromParams verifies the template-first / hardcoded-default rule.
func TestSubAllowFromParams(t *testing.T) {
	// Configured subscriptions win, source preserved.
	ws := parseDeclarations(t, `{"workers":[{"type":"reason","id":"r","subscriptions":["worker.ready",{"type":"request.completed","source":"niq"}]}]}`)
	_, got := aclFor(SpawnConfig(ws[0]), nil, []event.EventPattern{event.NewPattern("worker.discover")})
	if len(got) != 2 || got[0].Type != "worker.ready" || got[1].SourceID != "niq" {
		t.Fatalf("configured subs not honored: %+v", got)
	}

	// No config → hardcoded defaults.
	empty := parseDeclarations(t, `{"workers":[{"type":"reason","id":"r"}]}`)
	_, got = aclFor(SpawnConfig(empty[0]), nil, []event.EventPattern{event.NewPattern("worker.discover")})
	if len(got) != 1 || got[0].Type != "worker.discover" {
		t.Fatalf("defaults not used: %+v", got)
	}
}

// TestPublishSpecs verifies publish config entries parse and normalize: a bare
// string grant is unrestricted (target "*"), and an object may add a target
// restriction.
func TestPublishSpecs(t *testing.T) {
	ws := parseDeclarations(t, `{"workers":[{"type":"reason","id":"r","publish":["request.completed",{"type":"worker.discover","target":"ws-1"}]}]}`)
	specs := ws[0].Publish
	if len(specs) != 2 {
		t.Fatalf("publish = %+v, want 2", specs)
	}
	if specs[0].Type != "request.completed" || !specs[0].ToPublishPattern().BroadcastAllowed("request.completed") {
		t.Fatalf("bare publish spec not unrestricted: %+v", specs[0])
	}
	if specs[1].Type != "worker.discover" || specs[1].Target != "ws-1" {
		t.Fatalf("targeted publish spec wrong: %+v", specs[1])
	}
	if specs[1].ToPublishPattern().BroadcastAllowed("worker.discover") {
		t.Fatalf("targeted publish spec must not broadcast: %+v", specs[1])
	}

	// The declaration's publish specs lower to the identity ACL.
	got := SpawnConfig(ws[0]).PublishAllow
	if len(got) != 2 || got[0].Target != "*" || got[1].Target != "ws-1" {
		t.Fatalf("publish patterns = %+v, want [* @ws-1]", got)
	}
}

// parseDeclarations decodes the worker list of a template/config body. It is
// deliberately not the template package's parser: these tests are about how a
// DECLARATION's ACL fields lower into bus patterns, and depending on the layer
// above would invert the dependency for nothing.
func parseDeclarations(t *testing.T, raw string) []Declaration {
	t.Helper()
	var cfg struct {
		Workers []Declaration `json:"workers"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("parse declarations: %v", err)
	}
	return cfg.Workers
}
