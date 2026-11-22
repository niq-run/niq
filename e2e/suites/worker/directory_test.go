//go:build e2e

package worker_e2e

import (
	"testing"
	"time"
)

// TestE2EDirectoryRosterAnswersWhoIsHere: with no query the directory is the
// fleet roster — every worker id and type on the bus, which is what an agent
// asks before deciding whom to call.
func TestE2EDirectoryRosterAnswersWhoIsHere(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)
	stream := g.Watch(t, project, "")

	reqID := sendEvent(t, g, project, "directory", "search", nil)
	reply := stream.Await(t, "the roster reply", 15*time.Second, repliesTo(reqID))

	// The default template's workers are all on this bus.
	mustContain(t, reply, "timer", "directory", "host", "program")
}

// TestE2EDirectorySearchFindsByKeyword: a query narrows the roster and reports
// where it matched, so a caller can confirm before invoking anything.
func TestE2EDirectorySearchFindsByKeyword(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)
	stream := g.Watch(t, project, "")

	reqID := sendEvent(t, g, project, "directory", "search", map[string]any{"query": "timer"})
	reply := stream.Await(t, "the search reply", 15*time.Second, repliesTo(reqID))

	mustContain(t, reply, "worker_id", "timer")
	if got := payloadOf(reply); !containsText(got, `"total"`) {
		t.Fatalf("the reply does not report a total: %s", got)
	}
}

// TestE2EDirectoryDescribeExplainsOneWorker: describe is the follow-up to a
// search hit — the one worker's tools and events, not the whole fleet.
func TestE2EDirectoryDescribeExplainsOneWorker(t *testing.T) {
	t.Parallel()
	_, g, project := newProject(t)
	stream := g.Watch(t, project, "")

	reqID := sendEvent(t, g, project, "directory", "describe", map[string]any{"worker": "timer"})
	reply := stream.Await(t, "the describe reply", 15*time.Second, repliesTo(reqID))

	mustContain(t, reply, "timer")
	if got := payloadOf(reply); !containsText(got, "elapse") {
		t.Fatalf("describe does not mention the timer's tools: %s", got)
	}
}
