package timer

import (
	"context"
	"encoding/json"
	"time"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// timerSpec is everything needed to run — and, after a restart, to re-run — a
// single timer. It is the persisted unit: Snapshot() serializes the pending
// specs and Restore() re-arms them. IntervalMs > 0 makes it a recurring timer
// that re-arms itself each fire until cancelled; 0 is a one-shot timer.
type timerSpec struct {
	ID         string    `json:"id"`
	CallerID   string    `json:"caller_id"`
	TickType   string    `json:"tick_type"`
	Purpose    string    `json:"purpose"`
	TraceID    string    `json:"trace_id,omitempty"`
	FireAt     time.Time `json:"fire_at"` // the timer fires at this wall-clock time
	DurationMs int64     `json:"duration_ms"`
	IntervalMs int64     `json:"interval_ms,omitempty"`
}

// recurring reports whether this is an interval (self-rearming) timer.
func (s timerSpec) recurring() bool { return s.IntervalMs > 0 }

func (s timerSpec) delay() time.Duration {
	// time.AfterFunc panics on a negative duration; clamp a stale fire_at
	// (already in the past) so it fires on the next tick instead.
	if d := time.Until(s.FireAt); d > 0 {
		return d
	}
	return 0
}

// reminder reports whether this timer is a reminder — a gentle wake-up the
// caller receives as an input — rather than a tool-call timeout, a preemptive
// signal the caller handles specially.
func (s timerSpec) reminder() bool {
	return s.TickType == "reminder" || s.TickType == "interval"
}

// intervalOrNil renders the interval as a JSON number when recurring, or nil so
// one-shot timers omit the interval_ms field from the fired result.
func intervalOrNil(intervalMs int64) any {
	if intervalMs > 0 {
		return intervalMs
	}
	return nil
}

// Entry is the opaque handle returned by schedule.
type Entry struct {
	t    *time.Timer
	spec timerSpec
}

func (e *Entry) Stop() bool {
	if e.t == nil {
		return false
	}
	return e.t.Stop()
}

// schedule arms a timer to fire the appropriate event when it elapses (see
// fireEvent). onFire, if non-nil, is invoked on the timer's goroutine after
// the event is sent so the caller can drop the entry from its index and signal
// persistence.
func schedule(
	workerID string,
	bus corebus.WorkerSideChannel,
	spec timerSpec,
	onFire func(timerSpec),
) *Entry {
	t := time.AfterFunc(spec.delay(), func() {
		evt := fireEvent(workerID, spec)
		evt.TraceID = spec.TraceID
		_ = bus.Send(context.Background(), evt, spec.CallerID)
		if onFire != nil {
			onFire(spec)
		}
	})

	return &Entry{t: t, spec: spec}
}

// fireEvent builds the event a firing timer delivers to its caller. A reminder
// is a gentle wake-up, delivered as a worker.input (the caller's generic input)
// carrying the purpose as text; a tool-call timeout is a preemptive signal,
// delivered as timer.timeout with the timer id and the structured result.
func fireEvent(workerID string, spec timerSpec) event.Event {
	if spec.reminder() {
		text := spec.Purpose
		if text == "" {
			text = "[reminder]"
		}
		return event.New(event.TypeWorkerInput, workerID, map[string]any{
			"text": text,
		})
	}
	result, _ := json.Marshal(map[string]any{
		"tick_type":   spec.TickType,
		"purpose":     spec.Purpose,
		"duration_ms": spec.DurationMs,
		"fire_at":     spec.FireAt.Format(time.RFC3339),
		"interval_ms": intervalOrNil(spec.IntervalMs),
	})
	return event.New(event.EventType("timer.timeout"), workerID, map[string]any{
		"timer_id":  spec.ID,
		"caller_id": spec.CallerID,
		"result":    string(result),
	})
}
