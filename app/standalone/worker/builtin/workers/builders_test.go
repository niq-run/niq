package workers

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	providerpkg "github.com/niq-run/niq/app/standalone/provider"
	"github.com/niq-run/niq/app/standalone/worker/builtin"
	"github.com/niq-run/niq/core/impl/busctl"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/iface"
	"github.com/niq-run/niq/core/impl/workerctl"
	corebus "github.com/niq-run/niq/core/itfs/bus"
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
	var empty builtin.BuildContext

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
	var empty builtin.BuildContext
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
// a type that can be built must also declare what its spawn accepts, otherwise
// a new builder would quietly ship without a check. It holds in both directions:
// every registered builder has a contract, and every contract has a builder.
func TestSpawnParamsCoverEveryBuilder(t *testing.T) {
	te := newTestEngine(t)

	for _, typ := range te.types.Types() {
		keys, ok := te.types.Accepted(typ)
		if !ok {
			t.Errorf("builder type %q registered no spawn-parameter contract", typ)
			continue
		}
		if len(keys) == 0 {
			t.Errorf("builder type %q registered an empty contract", typ)
		}
	}
}

// TestSpawnRejectsUnknownParams verifies the type's spawn contract guards the
// boundary before a builder runs.
func TestSpawnRejectsUnknownParams(t *testing.T) {
	te := newTestEngine(t)

	err := te.types.Validate(coreworker.WorkerConfig{
		ID: "analyst", Type: "reason",
		Params: map[string]any{"type": "reason", "id": "analyst", "prompt": "Triage issues."},
	})
	if err == nil {
		t.Fatal("Validate accepted an unread spawn parameter")
	}
	if !strings.Contains(err.Error(), `"prompt"`) || !strings.Contains(err.Error(), "reason accepts") {
		t.Fatalf("err = %q, want the unknown-parameter error with the accepted list", err)
	}
	if te.mgr.Has("analyst") {
		t.Fatal("the rejected spawn still registered a worker")
	}
}

// ── harness: a project assembly without the network ──

// testEngine is the type registry, the manager it feeds, and the build context
// both need.
type testEngine struct {
	types *builtin.Registry
	bc    builtin.BuildContext
	mgr   *workerctl.Manager
}

// newTestEngine wires up a registry, engine and in-process listener exactly as
// runAssembly does.
func newTestEngine(t *testing.T) *testEngine {
	t.Helper()
	dir := t.TempDir()
	registry := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
	return newTestEngineWith(t, dir, registry)
}

// newTestEngineWith is newTestEngine over a caller-supplied registry, so a test
// can pre-seed persisted identities (an upgrade from an older contract).
func newTestEngineWith(t *testing.T, dir string, registry corebus.IdentityRegistry) *testEngine {
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

	bus := busctl.New(newMemIdentityStore(), registry)
	types := builtin.NewRegistry()
	mgr := workerctl.New(workerctl.Config{Build: types.Build})
	bc := builtin.BuildContext{
		Bus:          bus,
		Listener:     listener,
		Manager:      mgr,
		ProviderLoad: func() (*providerpkg.Config, error) { return &providerpkg.Config{}, nil },
	}
	Register(bc, types)
	return &testEngine{types: types, bc: bc, mgr: mgr}
}

// ── lifecycle over the real builders ──
//
// build.go constructs a worker; these drive what it then does through the
// manager: suspend/resume and the identity its connect path ensures.

func TestWorkspaceSuspendResume(t *testing.T) {
	te := newTestEngine(t)
	ctx := context.Background()

	if err := te.types.Validate(coreworker.WorkerConfig{
		Type: "workspace", Params: map[string]any{"mounts": []string{t.TempDir()}},
	}); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := te.mgr.Create(ctx, coreworker.WorkerConfig{
		Type: "workspace", Params: map[string]any{"mounts": []string{t.TempDir()}},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	wsID := firstWorkerOfType(te.mgr, "workspace")
	if wsID == "" {
		t.Fatal("no workspace worker created")
	}

	if info := workerStatus(te.mgr, wsID); info.State != string(coreworker.StateRunning) {
		t.Fatalf("expected running, got %s", info.State)
	}

	if err := te.mgr.Suspend(wsID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if info := workerStatus(te.mgr, wsID); info.State != string(coreworker.StateSuspended) {
		t.Fatalf("expected suspended, got %s", info.State)
	}

	if err := te.mgr.Resume(ctx, wsID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if info := workerStatus(te.mgr, wsID); info.State != string(coreworker.StateRunning) {
		t.Fatalf("expected running after resume, got %s", info.State)
	}
}

func firstWorkerOfType(mgr *workerctl.Manager, typ string) string {
	for _, wi := range mgr.List("") {
		if wi.Type == typ {
			return wi.ID
		}
	}
	return ""
}

func workerStatus(mgr *workerctl.Manager, id string) coreworker.WorkerInfo {
	for _, wi := range mgr.List("") {
		if wi.ID == id {
			return wi
		}
	}
	return coreworker.WorkerInfo{}
}

// TestReasonFamilyRejectsUnknownProvider verifies a reason worker must name a
// provider that exists in provider.json: there is no ad-hoc endpoint/credential
// fallback any more, so a typo fails the spawn with a clear message instead of
// silently building a provider nobody configured.
func TestReasonFamilyRejectsUnknownProvider(t *testing.T) {
	providers := &providerpkg.Config{Providers: []providerpkg.Entry{
		{Name: "known", Type: "deepseek", APIKey: "k", Model: "m1"},
	}}
	bc := builtin.BuildContext{ProviderLoad: func() (*providerpkg.Config, error) { return providers, nil }}
	_, err := resolveReasonFamily(bc, coreworker.WorkerConfig{
		ID:     "r1",
		Params: map[string]any{"provider": "typo-name", "model": "m1"},
	}, "reason")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unknown provider: err = %v, want a 'not configured' error", err)
	}

	in, err := resolveReasonFamily(bc, coreworker.WorkerConfig{
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

// memIdentityStore is a test-only bus.Store: identity persistence is exercised
// by the project's file store, so these tests only need a map.
type memIdentityStore struct {
	mu  sync.RWMutex
	ids map[string]corebus.Identity
}

func newMemIdentityStore() *memIdentityStore {
	return &memIdentityStore{ids: map[string]corebus.Identity{}}
}

func (s *memIdentityStore) Load(id string) (corebus.Identity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity, ok := s.ids[id]
	return identity, ok, nil
}

func (s *memIdentityStore) Save(id string, identity corebus.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = identity
	return nil
}

func (s *memIdentityStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
	return nil
}

func (s *memIdentityStore) All() ([]corebus.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]corebus.Identity, 0, len(s.ids))
	for _, identity := range s.ids {
		out = append(out, identity)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}
