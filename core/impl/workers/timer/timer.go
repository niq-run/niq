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

func (s timerSpec) eventType() event.EventType {
	// Interval timers are reminders: they gently wake the caller on a cadence.
	if s.TickType == "reminder" || s.TickType == "interval" {
		return event.EventType("timer.reminder")
	}
	return event.EventType("timer.timeout")
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

// schedule arms a timer to publish the appropriate timer.timeout /
// timer.reminder event when it fires. onFire, if non-nil, is invoked on the
// timer's goroutine after the event is sent so the caller can drop the entry
// from its index and signal persistence.
func schedule(
	workerID string,
	bus corebus.WorkerSideChannel,
	spec timerSpec,
	onFire func(timerSpec),
) *Entry {
	result, _ := json.Marshal(map[string]any{
		"tick_type":   spec.TickType,
		"purpose":     spec.Purpose,
		"duration_ms": spec.DurationMs,
		"fire_at":     spec.FireAt.Format(time.RFC3339),
		"interval_ms": intervalOrNil(spec.IntervalMs),
	})

	t := time.AfterFunc(spec.delay(), func() {
		evt := event.New(spec.eventType(), workerID, map[string]any{
			"timer_id":  spec.ID,
			"caller_id": spec.CallerID,
			"result":    string(result),
		})
		evt.TraceID = spec.TraceID
		_ = bus.Send(context.Background(), evt, spec.CallerID)
		if onFire != nil {
			onFire(spec)
		}
	})

	return &Entry{t: t, spec: spec}
}
