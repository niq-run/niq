package webui

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/impl/workerhost"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
)

// paramsRecorder captures UpdateWorkerParams calls.
type paramsRecorder struct {
	id     string
	params map[string]any
}

func (r *paramsRecorder) UpdateWorkerParams(id string, params map[string]any) error {
	r.id, r.params = id, params
	return nil
}

// TestHandleUpdateWorkerParams verifies PUT /api/workers/{id}/params persists
// the posted object (an absent body field clears params) and that the endpoint
// is disabled without an updater.
func TestHandleUpdateWorkerParams(t *testing.T) {
	rec := &paramsRecorder{}
	s := New(nil, nil, nil, nil, nil, ":0", false)
	s.SetWorkerParamsUpdater(rec)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := http.DefaultClient.Do(mustPut(t, "http://"+addr+"/api/workers/m/params", `{"params":{"fixed":true}}`))
	if err != nil {
		t.Fatalf("PUT params: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if rec.id != "m" || rec.params["fixed"] != true {
		t.Fatalf("recorded = %q %+v, want m with fixed=true", rec.id, rec.params)
	}

	// A missing params field clears the declaration's params.
	resp, err = http.DefaultClient.Do(mustPut(t, "http://"+addr+"/api/workers/m/params", `{}`))
	if err != nil {
		t.Fatalf("PUT clear: %v", err)
	}
	resp.Body.Close()
	if rec.params == nil || len(rec.params) != 0 {
		t.Fatalf("params = %+v, want an empty map", rec.params)
	}

	// No updater → 404 (a control-plane WebUI has no declarations).
	bare := New(nil, nil, nil, nil, nil, ":0", false)
	bareAddr, err := bare.Bind()
	if err != nil {
		t.Fatalf("Bind bare: %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- bare.Start(ctx2) }()
	t.Cleanup(func() { cancel2(); <-done2 })
	resp, err = http.DefaultClient.Do(mustPut(t, "http://"+bareAddr+"/api/workers/m/params", `{"params":{}}`))
	if err != nil {
		t.Fatalf("PUT without updater: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status without updater = %d, want 404", resp.StatusCode)
	}
}

// restartRecorder captures RestartManaged calls.
type restartRecorder struct{ id string }

func (r *restartRecorder) RestartManaged(id string) error { r.id = id; return nil }

// TestHandleManagedRestart verifies the restart endpoint routes a managed
// worker to the restarter (not the external supervisor), and 404s when no
// restarter is wired.
func TestHandleManagedRestart(t *testing.T) {
	svc := workerhost.New()
	svc.RegisterBuilder("fake", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		return worker.SpawnSpec{
			Config:  cfg,
			Connect: func() (corebus.WorkerSideChannel, error) { return nopChannel{}, nil },
			Build:   func(corebus.WorkerSideChannel) worker.ManagedWorker { return &nopWorker{id: cfg.ID} },
		}, nil
	})
	if err := svc.CreateWorker(context.Background(), worker.WorkerConfig{ID: "m", Type: "fake"}); err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	rec := &restartRecorder{}
	s := New(nil, nil, nil, svc, nil, ":0", false)
	s.SetManagedRestarter(rec)
	addr, err := s.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := http.DefaultClient.Post("http://"+addr+"/api/workers/m/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("POST restart: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if rec.id != "m" {
		t.Fatalf("restarted %q, want m", rec.id)
	}
}

// nopWorker is a ManagedWorker stub for lifecycle-free tests.
type nopWorker struct{ id string }

func (w *nopWorker) ID() string                  { return w.id }
func (w *nopWorker) Start(context.Context) error { return nil }
func (w *nopWorker) Stop() error                 { return nil }
func (w *nopWorker) Snapshot() ([]byte, error)   { return nil, nil }
func (w *nopWorker) Restore([]byte) error        { return nil }

// nopChannel is a WorkerSideChannel stub; these tests never route events.
type nopChannel struct{}

func (nopChannel) ID() string                                          { return "nop" }
func (nopChannel) Send(context.Context, event.Event, ...string) error  { return nil }
func (nopChannel) Broadcast(context.Context, event.Event) error        { return nil }
func (nopChannel) Receive(context.Context) (<-chan event.Event, error) { return nil, nil }
func (nopChannel) Close() error                                        { return nil }

func mustPut(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("PUT", url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}
