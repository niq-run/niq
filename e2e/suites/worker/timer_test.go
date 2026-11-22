//go:build e2e

package worker_e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestE2ETimerReminderWakesTheCaller exercises a real scheduled job: the elapse
// tool schedules a reminder, the timer fires it, and the wake-up arrives as a
// worker.input carrying the purpose that was asked for.
func TestE2ETimerReminderWakesTheCaller(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)
	stream := g.Watch(t, project, "")

	reqID := sendEvent(t, g, project, "timer", "elapse", map[string]any{
		"duration_ms": 150,
		"purpose":     "e2e-reminder",
	})

	// The tool answers at once, with the timer parked.
	reply := stream.Await(t, "the scheduling reply", 15*time.Second, repliesTo(reqID))
	mustContain(t, reply, "scheduled")

	// Then it fires on its own.
	wake := stream.Await(t, "the reminder", 20*time.Second, func(evt event.Event) bool {
		return evt.Type == event.TypeWorkerInput && containsPurpose(evt, "e2e-reminder")
	})
	if wake.WorkerId != "timer" {
		t.Fatalf("the wake-up came from %q, want the timer worker", wake.WorkerId)
	}
}

// TestE2ETimerListsWhatItHasScheduled: `list` is how an agent checks whether a
// timer survived, so it has to report the parked ones.
func TestE2ETimerListsWhatItHasScheduled(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)
	stream := g.Watch(t, project, "")

	sendEvent(t, g, project, "timer", "elapse", map[string]any{
		"duration_ms": 60_000, // long enough to still be pending
		"purpose":     "e2e-pending",
	})

	reqID := sendEvent(t, g, project, "timer", "list", nil)
	reply := stream.Await(t, "the list reply", 15*time.Second, repliesTo(reqID))
	mustContain(t, reply, "e2e-pending")
}

// containsPurpose looks for the purpose anywhere in the payload: the timer's
// wake-up nests it, and the exact shape is the worker's to change.
func containsPurpose(evt event.Event, purpose string) bool {
	return containsText(payloadOf(evt), purpose)
}

func containsText(haystack, needle string) bool {
	return len(haystack) > 0 && strings.Contains(haystack, needle)
}
