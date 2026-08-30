package httptrans

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// WorkerSide implements WorkerSideChannel over HTTP transport.
//
// It connects to the bus via SSE, sends events via POST /publish,
// and receives events via the SSE stream. Every request carries its
// own credential — there is no session token.
type WorkerSide struct {
	baseURL    string
	workerID   string
	credential string
	client     *stdhttp.Client

	mu       sync.Mutex
	connCh   chan event.Event
	connected bool
	cancel    context.CancelFunc
	closed    bool
}

// NewWorkerSide creates a new HTTP transport WorkerSideChannel.
// Call Connect to establish the connection to the bus.
func NewWorkerSide(baseURL, workerID, credential string) *WorkerSide {
	return &WorkerSide{
		baseURL:    strings.TrimRight(baseURL, "/"),
		workerID:   workerID,
		credential: credential,
		client:     &stdhttp.Client{},
	}
}

func (w *WorkerSide) ID() string { return w.workerID }

// Connect establishes the connection to the bus by opening an SSE stream.
// The credential is sent as a query parameter — the server validates it
// before creating the BusSideChannel.
func (w *WorkerSide) Connect(ctx context.Context, endpoint string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.connected {
		return nil
	}

	connCtx, cancel := context.WithCancel(ctx)
	ch, err := w.openSSE(connCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("httptrans: connect: %w", err)
	}

	w.connCh = ch
	w.cancel = cancel
	w.connected = true
	return nil
}

func (w *WorkerSide) openSSE(ctx context.Context) (chan event.Event, error) {
	url := fmt.Sprintf("%s/events?worker_id=%s&credential=%s",
		w.baseURL, w.workerID, w.credential)
	req, err := stdhttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("SSE connect failed (%d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan event.Event, 64)
	go w.readSSE(ctx, resp.Body, ch)
	return ch, nil
}

func (w *WorkerSide) readSSE(ctx context.Context, r io.ReadCloser, ch chan event.Event) {
	defer r.Close()
	defer close(ch)

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var evt event.Event
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue
		}
		select {
		case ch <- evt:
		case <-ctx.Done():
			return
		}
	}
}

func (w *WorkerSide) Send(ctx context.Context, evt event.Event, targets ...string) error {
	if !w.connected {
		return fmt.Errorf("httptrans: worker %s not connected", w.workerID)
	}
	if len(targets) == 0 {
		return fmt.Errorf("httptrans: Send requires at least one target")
	}
	return w.publish(ctx, "send", evt, targets)
}

func (w *WorkerSide) Broadcast(ctx context.Context, evt event.Event) error {
	if !w.connected {
		return fmt.Errorf("httptrans: worker %s not connected", w.workerID)
	}
	return w.publish(ctx, "broadcast", evt, nil)
}

func (w *WorkerSide) publish(ctx context.Context, typ string, evt event.Event, targets []string) error {
	body, _ := json.Marshal(publishRequest{
		WorkerID:   w.workerID,
		Credential: w.credential,
		Type:       typ,
		Events:     []event.Event{evt},
		Targets:    targets,
	})
	req, err := stdhttp.NewRequestWithContext(ctx, "POST",
		w.baseURL+"/publish", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("publish failed (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

func (w *WorkerSide) Receive(ctx context.Context) (<-chan event.Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.connected {
		return nil, fmt.Errorf("httptrans: worker %s not connected", w.workerID)
	}
	return w.connCh, nil
}

func (w *WorkerSide) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	w.connected = false
	if w.cancel != nil {
		w.cancel()
	}
	return nil
}

// Compile-time check.
var _ corebus.WorkerSideChannel = (*WorkerSide)(nil)