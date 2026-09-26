package timer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/impl/baseworker"
)

// Worker is the TimerWorker — a bus-connected timer service.
// Schedule calls return immediately; the actual timer fires a
// timer.timeout / timer.reminder trigger event later. Pending timers are
// persisted via Snapshot/Restore so they survive a restart.
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
	// OnDurableChange is invoked when the set of pending timers changed and
	// must survive a restart. Baseworker machinery; nil leaves signalling off.
	OnDurableChange func()
}

// The timer worker's tools, each its own event type (see the request-response
// convention).
const (
	TypeTimeout     event.EventType = "timeout"
	TypeElapse      event.EventType = "elapse"
	TypeAt          event.EventType = "at"
	TypeInterval    event.EventType = "interval"
	TypeList        event.EventType = "list"
	TypeCancelTimer event.EventType = "cancel"
)

// New creates a TimerWorker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "timer"
	}
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(id, cfg.Bus),
		timers:     make(map[string]*Entry),
	}
	// Durable-change signalling is the base's machinery (baseworker); the
	// mechanism only decides when to raise it (see arm / handleCancel).
	w.SetOnDurableChange(cfg.OnDurableChange)
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
		Event:       TypeAt,
		Description: "Set a reminder timer at a specific point in time instead of after a relative delay. When the wall-clock reaches the given time, you will receive a timer.reminder event carrying the purpose. Use this to schedule reminders/timers at an absolute time (e.g. 17:00 this evening), which survive a worker restart.",
		Parameters: obj(map[string]any{
			"fire_at": map[string]any{
				"type":        "string",
				"description": "RFC3339 timestamp, e.g. \"2026-09-16T17:00:00+08:00\" or \"2026-09-16T09:00:00Z\".",
			},
			"unix_ms": map[string]any{
				"type":        "integer",
				"description": "Alternative to fire_at: the wall-clock trigger time as epoch milliseconds.",
			},
			"purpose": map[string]any{
				"type":        "string",
				"description": "Natural language description of what to do when this timer fires.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleTickAt(tc.CallID, string(TypeAt), tc.CallerID, tc.Args, tc.TraceID)
	})

	w.Register(baseworker.Extension{
		Event:       TypeInterval,
		Description: "Set a repeating reminder. From the (optional) first fire time, or after interval_ms, the caller receives a timer.reminder event every interval_ms until the timer is cancelled. Use this for recurring reminders, unlike the one-shot elapse/at timers.",
		Parameters: obj(map[string]any{
			"interval_ms": map[string]any{
				"type":        "integer",
				"description": "Period between firings, in milliseconds. Must be positive.",
			},
			"fire_at": map[string]any{
				"type":        "string",
				"description": "Optional first fire time (RFC3339). Defaults to now + interval_ms.",
			},
			"unix_ms": map[string]any{
				"type":        "integer",
				"description": "Alternative to fire_at: epoch milliseconds for the first fire.",
			},
			"purpose": map[string]any{
				"type":        "string",
				"description": "Natural language description of what to do each time this timer fires.",
			},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleInterval(tc.CallID, string(TypeInterval), tc.CallerID, tc.Args, tc.TraceID)
	})

	w.Register(baseworker.Extension{
		Event:       TypeList,
		Description: "List all currently pending timers: id, tick_type, caller, purpose, and when each fires.",
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleList(tc.CallID, string(TypeList), tc.CallerID, tc.TraceID)
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
	{"type": "timer.reminder", "description": "An elapse/at reminder timer has fired"},
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
	// The durable-change signal (baseworker) delivers the persistence
	// callback off the event loop; no goroutine when nothing is installed.
	w.StartDurableLoop(runCtx)
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

// snapshotState is the persisted form of all pending timers.
type snapshotState struct {
	Timers []timerSpec `json:"timers"`
}

func (w *Worker) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	state := snapshotState{Timers: make([]timerSpec, 0, len(w.timers))}
	for _, e := range w.timers {
		state.Timers = append(state.Timers, e.spec)
	}
	// Deterministic output: earliest fire first.
	sort.Slice(state.Timers, func(i, j int) bool {
		return state.Timers[i].FireAt.Before(state.Timers[j].FireAt)
	})
	if len(state.Timers) == 0 {
		return nil, nil
	}
	return json.Marshal(state)
}

func (w *Worker) Restore(state []byte) error {
	if len(state) == 0 {
		return nil
	}
	var s snapshotState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("timer: restore: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	// Restore runs on a freshly-constructed worker (timers is empty), but
	// replace defensively so a stale set can never double-schedule.
	for _, e := range w.timers {
		e.Stop()
	}
	w.timers = make(map[string]*Entry, len(s.Timers))
	for _, spec := range s.Timers {
		w.timers[spec.ID] = w.arm(spec)
	}
	return nil
}

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
		if evt.WorkerId != w.ID() {
			w.AnnounceReadyTo(evt.WorkerId, "timer", timerPublishes, false)
		}
	case event.TypeRequestCancel:
		w.handleCancelEvent(evt)
	default:
		// Tool invocations (timeout / elapse / at / cancel) route here by
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
	if ok {
		w.NotifyDurableChange()
	}
}

// arm schedules a spec and wires the post-fire behaviour: on fire, a one-shot
// entry is dropped from the index, while a recurring (interval) entry is
// re-armed at its next fire time. The change is signalled for persistence in
// either case. The onFire callback runs on the timer's goroutine, so it takes
// w.mu itself (never at the caller's) before touching the index.
func (w *Worker) arm(spec timerSpec) *Entry {
	return schedule(w.ID(), w.Channel, spec, func(fired timerSpec) {
		w.mu.Lock()
		if fired.recurring() {
			// Cadence-anchored: advance by the interval from the fire time this
			// round was armed for, keeping the beat stable across firings.
			next := fired
			next.FireAt = fired.FireAt.Add(time.Duration(fired.IntervalMs) * time.Millisecond)
			w.timers[fired.ID] = w.arm(next)
		} else {
			delete(w.timers, fired.ID)
		}
		w.mu.Unlock()
		w.NotifyDurableChange()
	})
}

// scheduleRelative arms a timer firing `delay` from now.
func (w *Worker) scheduleRelative(spec timerSpec, delay time.Duration) {
	spec.FireAt = time.Now().Add(delay)
	spec.DurationMs = delay.Milliseconds()
	w.mu.Lock()
	w.timers[spec.ID] = w.arm(spec)
	w.mu.Unlock()
	w.NotifyDurableChange()
}

func (w *Worker) handleTickAfter(callID, toolName, callerID string, args map[string]any, traceID string) {
	durationMS := baseworker.ArgInt(args, "duration_ms", 0)
	purpose := baseworker.ArgString(args, "purpose")
	tickType := baseworker.ArgString(args, "tick_type")

	w.scheduleRelative(timerSpec{
		ID: callID, CallerID: callerID, TickType: tickType, Purpose: purpose, TraceID: traceID,
	}, time.Duration(durationMS)*time.Millisecond)

	result, _ := json.Marshal(map[string]any{
		"tick_type": tickType,
		"purpose":   purpose,
		"status":    "scheduled",
	})
	w.ReplyCompleted(callerID, callID, string(result), traceID)
}

// handleInterval schedules a recurring reminder. After an optional first fire
// time (fire_at / unix_ms) or after interval_ms, a timer.reminder fires every
// interval_ms until cancelled.
func (w *Worker) handleInterval(callID, toolName, callerID string, args map[string]any, traceID string) {
	intervalMS := baseworker.ArgInt(args, "interval_ms", 0)
	purpose := baseworker.ArgString(args, "purpose")
	if intervalMS <= 0 {
		w.ReplyFailed(callerID, callID, "interval: interval_ms must be positive", traceID)
		return
	}
	tickType := "interval"

	// First fire: at an explicit time if given, otherwise after one interval.
	first := time.Now().Add(time.Duration(intervalMS) * time.Millisecond)
	if t, ok := parseFireAt(args); ok {
		first = t.UTC()
	}

	spec := timerSpec{
		ID: callID, CallerID: callerID, TickType: tickType, Purpose: purpose, TraceID: traceID,
		FireAt: first, DurationMs: int64(intervalMS), IntervalMs: int64(intervalMS),
	}
	w.mu.Lock()
	w.timers[callID] = w.arm(spec)
	w.mu.Unlock()
	w.NotifyDurableChange()

	result, _ := json.Marshal(map[string]any{
		"tick_type":   tickType,
		"purpose":     purpose,
		"status":      "scheduled",
		"interval_ms": intervalMS,
		"fire_at":     first.Format(time.RFC3339),
	})
	w.ReplyCompleted(callerID, callID, string(result), traceID)
}

// parseFireAt reads the absolute trigger time from an "at" argument set. It
// accepts either fire_at (RFC3339 string) or unix_ms (epoch milliseconds).
func parseFireAt(args map[string]any) (time.Time, bool) {
	switch v := args["unix_ms"].(type) {
	case float64:
		return time.UnixMilli(int64(v)), true
	case int64:
		return time.UnixMilli(v), true
	case int:
		return time.UnixMilli(int64(v)), true
	}
	if s, _ := args["fire_at"].(string); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (w *Worker) handleTickAt(callID, toolName, callerID string, args map[string]any, traceID string) {
	purpose := baseworker.ArgString(args, "purpose")
	// "at" is always a gentle reminder, never a tool-call timeout.
	tickType := "reminder"

	fireAt, ok := parseFireAt(args)
	if !ok {
		errMsg := "at: expected fire_at (RFC3339) or unix_ms (epoch milliseconds)"
		w.ReplyFailed(callerID, callID, errMsg, traceID)
		return
	}
	fireAt = fireAt.UTC()

	spec := timerSpec{
		ID: callID, CallerID: callerID, TickType: tickType, Purpose: purpose, TraceID: traceID,
		FireAt: fireAt,
	}
	// Durations here represent the wait until the absolute trigger time.
	spec.DurationMs = time.Until(fireAt).Milliseconds()
	w.mu.Lock()
	w.timers[callID] = w.arm(spec)
	w.mu.Unlock()
	w.NotifyDurableChange()

	result, _ := json.Marshal(map[string]any{
		"tick_type": tickType,
		"purpose":   purpose,
		"status":    "scheduled",
		"fire_at":   fireAt.Format(time.RFC3339),
	})
	w.ReplyCompleted(callerID, callID, string(result), traceID)
}

// listTimers is the wire entry for one pending timer, as returned by the list
// tool.
type listTimers struct {
	Timers []map[string]any `json:"timers"`
}

func (w *Worker) handleList(callID, toolName, callerID, traceID string) {
	w.mu.Lock()
	items := make([]map[string]any, 0, len(w.timers))
	for _, e := range w.timers {
		items = append(items, map[string]any{
			"timer_id":    e.spec.ID,
			"caller_id":   e.spec.CallerID,
			"tick_type":   e.spec.TickType,
			"purpose":     e.spec.Purpose,
			"fire_at":     e.spec.FireAt.UTC().Format(time.RFC3339),
			"duration_ms": e.spec.DurationMs,
			"interval_ms": intervalOrNil(e.spec.IntervalMs),
		})
	}
	w.mu.Unlock()

	result, _ := json.Marshal(listTimers{Timers: items})
	w.ReplyCompleted(callerID, callID, string(result), traceID)
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
		w.NotifyDurableChange()
		w.ReplyCompleted(callerID, callID, `{"status":"cancelled"}`, traceID)
	} else {
		w.ReplyFailed(callerID, callID, "timer not found: "+timerID, traceID)
	}
}
