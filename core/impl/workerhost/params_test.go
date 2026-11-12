package workerhost

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/worker"
)

// TestCreateWorkerRejectsUnknownParams pins the spawn boundary: a parameter a
// type does not read fails the call before the builder runs, instead of
// producing a worker that silently ignores it.
func TestCreateWorkerRejectsUnknownParams(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(svc)
	svc.RegisterParamCheck("fake", func(params map[string]any) error {
		if _, ok := params["bogus"]; ok {
			return fmt.Errorf("unknown spawn parameter %q", "bogus")
		}
		return nil
	})

	err := svc.CreateWorker(context.Background(), worker.WorkerConfig{
		ID: "rejected", Type: "fake", Params: map[string]any{"bogus": 1},
	})
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("CreateWorker = %v, want the unknown-parameter error", err)
	}
	if len(*built) != 0 {
		t.Fatalf("builder ran %d time(s), want 0: the check must reject first", len(*built))
	}
	if svc.HasWorker("rejected") {
		t.Fatal("a rejected spawn must not register the worker")
	}
}

// TestCreateWorkerAcceptsCheckedParams verifies a passing check does not stop
// the normal path — the worker is built and running.
func TestCreateWorkerAcceptsCheckedParams(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(svc)
	svc.RegisterParamCheck("fake", func(map[string]any) error { return nil })

	if err := svc.CreateWorker(context.Background(), worker.WorkerConfig{ID: "ok", Type: "fake"}); err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	if len(*built) != 1 {
		t.Fatalf("built %d worker(s), want 1", len(*built))
	}
	if info := workerStatus(svc, "ok"); info.State != worker.StateRunning {
		t.Fatalf("state = %s, want running", info.State)
	}
}

// TestRestoreAndRunSkipsParamCheck pins that recovery is deliberately not gated
// by the spawn check: a declaration accepted by an earlier build must still
// start, even if a key it carries is no longer part of the spawn contract.
func TestRestoreAndRunSkipsParamCheck(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(svc)
	svc.RegisterParamCheck("fake", func(map[string]any) error {
		return fmt.Errorf("unknown spawn parameter %q", "legacy")
	})

	cfg := worker.WorkerConfig{ID: "legacy", Type: "fake", Params: map[string]any{"legacy": true}}
	if err := svc.RestoreAndRun(context.Background(), cfg, []byte("snap")); err != nil {
		t.Fatalf("RestoreAndRun = %v, want recovery to bypass the spawn check", err)
	}
	if len(*built) != 1 || !(*built)[0].started {
		t.Fatalf("built = %+v, want one started worker", *built)
	}
}

// TestRegisteredTypes verifies the builder registry is introspectable, which is
// what lets the assembly layer assert that every type it can build also has its
// per-type knowledge (spawn-parameter validation) registered.
func TestRegisteredTypes(t *testing.T) {
	svc := New()
	svc.RegisterBuilder("b", fakeBuilder)
	svc.RegisterBuilder("a", fakeBuilder)

	got := svc.RegisteredTypes()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("RegisteredTypes = %v, want [a b] (sorted)", got)
	}
}

func fakeBuilder(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	return worker.SpawnSpec{Config: cfg}, nil
}
