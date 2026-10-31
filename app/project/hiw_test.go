package project

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/workerhost"
	"github.com/niq-run/niq/core/impl/workers/hiw"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
)

// TestHIWAnnouncesApprovalCapability verifies the contract the running project
// must expose: webui-hiw may hear the fleet's discover broadcast, and the watch
// it announces carries approval.request with an argument schema — otherwise
// the approval flow is invisible to the directory roster and to any worker
// resolving "who approves boundary expansions".
func TestHIWAnnouncesApprovalCapability(t *testing.T) {
	bc, svc, _ := newTestEngine(t)
	ctx := context.Background()

	err := svc.RecoverAll(ctx, workerhost.RecoverOptions{
		Essential: []worker.WorkerConfig{{ID: webuiHIWID, Type: "hiw"}},
	})
	if err != nil {
		t.Fatalf("recover hiw: %v", err)
	}

	id, ok := bc.Registry.Lookup(webuiHIWID)
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

	h, ok := svc.Worker(webuiHIWID)
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
		WorkerID:     webuiHIWID,
		Type:         "hiw",
		PublishAllow: []event.PublishPattern{event.NewPublishPattern("*")},
	}
	if err := reg.Register(stale); err != nil {
		t.Fatalf("seed stale identity: %v", err)
	}

	bc, svc, _ := newTestEngineWith(t, dir, reg)
	err = svc.RecoverAll(context.Background(), workerhost.RecoverOptions{
		Essential: []worker.WorkerConfig{{ID: webuiHIWID, Type: "hiw"}},
	})
	if err != nil {
		t.Fatalf("recover hiw: %v", err)
	}

	id, ok := bc.Registry.Lookup(webuiHIWID)
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
