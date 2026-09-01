package timer

import (
	"context"
	"encoding/json"
	"time"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// Entry is the opaque handle returned by afterFunc.
type Entry struct {
	t *time.Timer
}

func (e *Entry) Stop() bool {
	if e.t == nil {
		return false
	}
	return e.t.Stop()
}

// afterFunc sets a timer that publishes a timer.elapsed trigger event when it fires.
func afterFunc(
	workerID string,
	bus corebus.WorkerSideChannel,
	timerID, callerID string,
	durationMS int,
	purpose, tickType, traceID string,
) *Entry {
	result, _ := json.Marshal(map[string]any{
		"tick_type":   tickType,
		"purpose":     purpose,
		"duration_ms": durationMS,
	})

	t := time.AfterFunc(time.Duration(durationMS)*time.Millisecond, func() {
		eventType := event.EventType("timer.timeout")
		if tickType == "reminder" {
			eventType = event.EventType("timer.reminder")
		}
		evt := event.New(eventType, workerID, map[string]any{
			"timer_id":  timerID,
			"caller_id": callerID,
			"result":    string(result),
		})
		evt.TraceID = traceID
		_ = bus.Send(context.Background(), evt, callerID)
	})

	return &Entry{t: t}
}
