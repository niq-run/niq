package timer

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/baseworker"
)

// Worker is the TimerWorker — a bus-connected timer service.
// tickafter returns immediately; the actual timer fires a timer.elapsed trigger event later.
type Worker struct {
	baseworker.BaseWorker
	timers    map[string]*Entry
	started   bool
	cancelRun context.CancelFunc
	mu        sync.Mutex
}

// Config holds TimerWorker configuration.
type Config struct {
	ID  string // worker ID, defaults to "timer"
	Bus corebus.WorkerSideChannel
}

// New creates a TimerWorker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "timer"
	}
	return &Worker{
		BaseWorker: baseworker.NewBaseWorker(id, []event.EventPattern{
			event.NewPattern(event.TypeToolRequest),
			event.NewPattern(event.TypeRequestCancel),
			event.NewPattern(event.TypeWorkerDiscover),
		}, cfg.Bus),
		timers: make(map[string]*Entry),
	}
}

// Start subscribes to the bus and begins watching for timer requests.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("timer: already started")
	}
	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancelRun = cancelFn

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.publishReady()
	w.started = true
	return nil
}

func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	for _, e := range w.timers {
		e.Stop()
	}
	w.cancelRun()
	w.cancelRun = nil
	w.started = false
	return nil
}

func (w *Worker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *Worker) Restore(state []byte) error { return nil }

func (w *Worker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) process(evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerDiscover:
		w.publishReady()
	case event.TypeToolRequest:
		if evt.TargetWorkerID != "" && evt.TargetWorkerID != w.ID() {
			return
		}
		w.handleToolCall(evt)
	case event.TypeRequestCancel:
		w.handleCancelEvent(evt)
	}
}

// handleCancelEvent stops a pending timer in response to a tool.cancel event
// (e.g. from a reason worker cancelling its set_tool_timeout once the watched
// tool completes). Payload carries the timer_id being cancelled.
func (w *Worker) handleCancelEvent(evt event.Event) {
	timerID, _ := evt.Payload["timer_id"].(string)
	if timerID == "" {
		return
	}
	w.mu.Lock()
	e, ok := w.timers[timerID]
	if ok {
		e.Stop()
		delete(w.timers, timerID)
	}
	w.mu.Unlock()
}

func (w *Worker) handleToolCall(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	args := tc.Args

	switch tc.Name {
	case "set_tool_timeout":
		// set_tool_timeout always uses tool_call_timeout tick_type.
		args["tick_type"] = "tool_call_timeout"
		w.handleTickAfter(tc.CallID, tc.Name, tc.CallerID, args, tc.TraceID)
	case "elapse":
		// elapse always uses reminder tick_type.
		args["tick_type"] = "reminder"
		w.handleTickAfter(tc.CallID, tc.Name, tc.CallerID, args, tc.TraceID)
	case "cancel":
		w.handleCancel(tc.CallID, tc.Name, tc.CallerID, args, tc.TraceID)
	default:
		w.ReplyUnknownTool(tc)
	}
}

func (w *Worker) handleTickAfter(callID, toolName, callerID string, args map[string]any, traceID string) {
	durationMS := baseworker.ArgInt(args, "duration_ms", 0)
	purpose := baseworker.ArgString(args, "purpose")
	tickType := baseworker.ArgString(args, "tick_type")

	w.mu.Lock()
	w.timers[callID] = afterFunc(w.ID(), w.Channel, callID, callerID,
		durationMS, purpose, tickType, traceID)
	w.mu.Unlock()

	result, _ := json.Marshal(map[string]any{
		"tick_type": tickType,
		"purpose":   purpose,
		"status":    "scheduled",
	})
	w.ReplyCompleted(callerID, callID, toolName, string(result), traceID)
}

func (w *Worker) handleCancel(callID, toolName, callerID string, args map[string]any, traceID string) {
	timerID := baseworker.ArgString(args, "timer_id")

	w.mu.Lock()
	e, ok := w.timers[timerID]
	if ok {
		e.Stop()
		delete(w.timers, timerID)
	}
	w.mu.Unlock()

	if ok {
		w.ReplyCompleted(callerID, callID, toolName, `{"status":"cancelled"}`, traceID)
	} else {
		w.ReplyFailed(callerID, callID, toolName, "timer not found: "+timerID, traceID)
	}
}

func (w *Worker) publishReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "timer",
		"tools": []map[string]any{{
			"name":        "set_tool_timeout",
			"description": "Set a timeout for your pending tool calls. When the timer fires, unresponsive tool calls will be automatically cancelled so you can proceed. If all tool calls complete before the timeout, the timer is cancelled automatically. Call this after issuing tool calls that may take a while.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"duration_ms": map[string]any{
						"type":        "integer",
						"description": "Timeout in milliseconds.",
					},
					"purpose": map[string]any{
						"type":        "string",
						"description": "Why this timeout is needed.",
					},
				},
				"required": []any{"duration_ms", "purpose"},
			},
		}, {
			"name":        "elapse",
			"description": "Set a reminder timer. After the specified duration, you will receive a timer.elapsed event. Unlike set_tool_timeout, this timer is never automatically cancelled — it always fires. Use this for general-purpose timing and reminders.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"duration_ms": map[string]any{
						"type":        "integer",
						"description": "Duration in milliseconds.",
					},
					"purpose": map[string]any{
						"type":        "string",
						"description": "Natural language description of what to do when this timer fires.",
					},
				},
				"required": []any{"duration_ms", "purpose"},
			},
		}},
		"publishes": []map[string]any{
			{"type": "timer.timeout", "description": "A set_tool_timeout timer has fired"},
			{"type": "timer.reminder", "description": "An elapse reminder timer has fired"},
		},
	}))
}
