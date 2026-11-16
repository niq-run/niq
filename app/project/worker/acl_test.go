package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// TestACLAdminPersistsThenApplies verifies the ordering that makes an ACL edit
// durable: the declaration is written first (workers.json is the source), then the
// runtime registry projection follows.
func TestACLAdminPersistsThenApplies(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	acl := &ACLAdmin{ProjectID: "test", Decls: rig.decls, Registry: rig.registry}

	pub := []event.PublishPattern{event.NewPublishPattern("worker.ready"), event.NewPublishPattern("request.*")}
	sub := []event.EventPattern{event.NewPattern("worker.discover")}
	if err := acl.UpdateACL("w1", pub, sub); err != nil {
		t.Fatalf("UpdateACL: %v", err)
	}

	d, ok, _ := rig.decls.Get("w1")
	if !ok || len(d.Publish) != 2 || d.Publish[1].Type != "request.*" {
		t.Fatalf("declaration publish = %+v", d)
	}
	if len(d.Subscriptions) != 1 || d.Subscriptions[0].Type != "worker.discover" {
		t.Fatalf("declaration subscriptions = %+v", d.Subscriptions)
	}

	idt, ok := rig.registry.Lookup("w1")
	if !ok || len(idt.PublishAllow) != 2 || len(idt.SubscribeAllow) != 1 {
		t.Fatalf("registry identity = %+v, ok=%v", idt, ok)
	}
}

// TestACLAdminKeepsDeclarationIntact verifies an ACL edit rewrites only the
// grants: params, placement, launch spec and credential survive.
func TestACLAdminKeepsDeclarationIntact(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		Placement:    coreworker.PlacementManagedProcess,
		Command:      []string{"/bin/sh", "-c", "sleep 60"},
		WorkerConfig: coreworker.WorkerConfig{ID: "proc", Type: "mcp", Params: map[string]any{"K": "V"}, Label: "进程"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	acl := &ACLAdmin{ProjectID: "test", Decls: rig.decls, Registry: rig.registry}
	if err := acl.UpdateACL("proc", []event.PublishPattern{event.NewPublishPattern("worker.ready")}, nil); err != nil {
		t.Fatalf("UpdateACL: %v", err)
	}
	d, _, _ := rig.decls.Get("proc")
	if d.Params["K"] != "V" || d.Label != "进程" || len(d.Command) != 3 || PlacementOf(*d) != coreworker.PlacementManagedProcess {
		t.Fatalf("ACL edit damaged the declaration: %+v", d)
	}
}

// TestACLAdminUnknownWorker verifies an undeclared worker is refused, and that a
// declared-but-never-started worker (no identity yet) is still editable: the next
// start registers the identity and picks the persisted grant up.
func TestACLAdminUnknownWorker(t *testing.T) {
	rig := newTestRig(t)
	acl := &ACLAdmin{ProjectID: "test", Decls: rig.decls, Registry: rig.registry}
	if err := acl.UpdateACL("ghost", nil, nil); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("err = %v, want a not-declared error", err)
	}

	d := Declaration{Config: Config{ID: "idle", Type: "fake"}}
	if err := rig.decls.Add(&d); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok := rig.registry.Lookup("idle"); ok {
		t.Fatal("unexpected identity for a never-started worker")
	}
	if err := acl.UpdateACL("idle", []event.PublishPattern{event.NewPublishPattern("worker.ready")}, nil); err != nil {
		t.Fatalf("UpdateACL on a declared worker: %v", err)
	}
	got, _, _ := rig.decls.Get("idle")
	if len(got.Publish) != 1 {
		t.Fatalf("declaration publish = %+v", got.Publish)
	}
}

// TestDeclarationsIsActiveOnly verifies the catalog's declaration view carries
// labels and params for active workers and drops archived ones.
func TestDeclarationsIsActiveOnly(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake", Params: map[string]any{"name": "a"}, Label: "值班"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w2", Type: "fake"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := rig.backend.Declarations()
	if len(got) != 2 {
		t.Fatalf("declarations = %+v", got)
	}
	if got["w1"].Label != "值班" || got["w1"].Params["name"] != "a" {
		t.Fatalf("view = %+v", got["w1"])
	}

	if err := rig.backend.Archive(ctx, "w2", true); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	got = rig.backend.Declarations()
	if _, ok := got["w2"]; ok {
		t.Fatalf("archived worker still declared in the active view: %+v", got)
	}
}
