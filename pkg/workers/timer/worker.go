package timer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// The timer worker's tools, each its own event type (see the request-response
// convention).
const (
	TypeTimeout     event.EventType = "timeout"
	TypeElapse      event.EventType = "elapse"
	TypeCancelTimer event.EventType = "cancel"
)

// New creates a TimerWorker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "timer"
	}
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(id, []event.EventPattern{
			event.NewPattern(TypeTimeout),
			event.NewPattern(TypeElapse),
			event.NewPattern(TypeCancelTimer),
			event.NewPattern(event.TypeWorkerDiscover),
			event.NewPattern(event.TypeRequestCancel),
		}, cfg.Bus),
		timers: make(map[string]*Entry),
	}
	w.registerExtensions()
	return w
}

// registerExtensions declares the timer tools: each is an extension served by
// its own event type, announced to peers via AnnounceReady.
func (w *Worker) registerExtensions() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.Register(baseworker.Extension{
		Event:       TypeTimeout,
		Description: "Set a timeout for your pending tool calls. When the timer fires, unresponsive tool calls will be automatically cancelled so you can proceed. If all tool calls complete before the timeout, the timer is cancelled automatically. Call this after issuing tool calls that may take a while.",
		Parameters: obj(map[string]any{
			"duration_ms": map[string]any{
				"type":        "integer",
				"description": "Timeout in milliseconds.",
			},
			"purpose": map[string]any{
				"type":        "string",
				"description": "Why this timeout is needed.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		// timeout always uses tool_call_timeout tick_type.
		tc.Args["tick_type"] = "tool_call_timeout"
		w.handleTickAfter(tc.CallID, string(TypeTimeout), tc.CallerID, tc.Args, tc.TraceID)
	})

	w.Register(baseworker.Extension{
		Event:       TypeElapse,
		Description: "Set a reminder timer. After the specified duration, you will receive a timer.elapsed event. Unlike the timeout tool, this timer is never automatically cancelled — it always fires. Use this for general-purpose timing and reminders.",
		Parameters: obj(map[string]any{
			"duration_ms": map[string]any{
				"type":        "integer",
				"description": "Duration in milliseconds.",
			},
			"purpose": map[string]any{
				"type":        "string",
				"description": "Natural language description of what to do when this timer fires.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		// elapse always uses reminder tick_type.
		tc.Args["tick_type"] = "reminder"
		w.handleTickAfter(tc.CallID, string(TypeElapse), tc.CallerID, tc.Args, tc.TraceID)
	})

	w.Register(baseworker.Extension{
		Event:       TypeCancelTimer,
		Description: "Cancel a pending timer by its timer_id.",
		Parameters: obj(map[string]any{
			"timer_id": map[string]any{
				"type":        "string",
				"description": "The timer to cancel.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleCancel(tc.CallID, string(TypeCancelTimer), tc.CallerID, tc.Args, tc.TraceID)
	})
}

// timerPublishes are the events the timer declares it emits, shown to peers
// via list_workers.
var timerPublishes = []map[string]any{
	{"type": "timer.timeout", "description": "A timeout timer has fired"},
	{"type": "timer.reminder", "description": "An elapse reminder timer has fired"},
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
	w.AnnounceReady("timer", timerPublishes)
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
		w.AnnounceReady("timer", timerPublishes)
	case event.TypeRequestCancel:
		w.handleCancelEvent(evt)
	default:
		// Tool invocations (timeout / elapse / cancel) route here by
		// their own event type.
		if !w.DispatchExtension(evt) {
			log.Printf("[timer %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}

// handleCancelEvent stops a pending timer in response to a request.cancel
// event (e.g. from a reason worker cancelling its timeout once the
// watched tool completes). Payload carries the timer_id being cancelled.
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
