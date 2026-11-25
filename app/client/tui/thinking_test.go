package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/contract"
)

// TestThinkingBlockShort pins a trace that fits: all rows shown, grey, first row
// carrying the label.
func TestThinkingBlockShort(t *testing.T) {
	r := thinkingBlock("one\ntwo\nthree", 40)
	if len(r.body) != 3 {
		t.Fatalf("rows = %d, want 3: %q", len(r.body), r.body)
	}
	plain := stripANSI(strings.Join(r.body, "\n"))
	if !strings.HasPrefix(plain, " thinking: one\n two\n three") {
		t.Fatalf("body = %q", plain)
	}
	if r.band != bandNone {
		t.Fatalf("band = %v, want bandNone", r.band)
	}
}

// TestThinkingBlockClipsToTen pins the display budget: a long trace keeps its
// first three rows, says how many it hid, and keeps the newest seven.
func TestThinkingBlockClipsToTen(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	r := thinkingBlock(strings.TrimSpace(b.String()), 40)
	// 3 head + marker + 7 tail
	if len(r.body) != thinkingHead+1+thinkingTail {
		t.Fatalf("rows = %d, want %d: %q", len(r.body), thinkingHead+1+thinkingTail, r.body)
	}
	plain := stripANSI(strings.Join(r.body, "\n"))
	for _, want := range []string{"line 1", "line 2", "line 3", "20 lines omitted", "line 24", "line 30"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	for _, gone := range []string{"line 4\n", "line 20\n"} {
		if strings.Contains(plain+"\n", gone) {
			t.Errorf("kept %q", gone)
		}
	}
	// The oldest head row comes first; the newest row comes last.
	if !strings.Contains(r.body[0], "line 1") || !strings.Contains(r.body[len(r.body)-1], "line 30") {
		t.Fatalf("head/tail order wrong: %q", r.body)
	}
}

// TestThinkingStreamsTail pins the streaming behavior: as deltas arrive the block
// grows, and past ten rows the newest rows stay visible while the middle is
// summarized.
func TestThinkingStreamsTail(t *testing.T) {
	var live strings.Builder
	var last string
	for i := 1; i <= 15; i++ {
		fmt.Fprintf(&live, "step %d\n", i)
		last = strings.Join(thinkingBlock(live.String(), 40).body, "\n")
	}
	if !strings.Contains(stripANSI(last), "step 15") {
		t.Fatalf("newest step not visible:\n%s", stripANSI(last))
	}
	if !strings.Contains(stripANSI(last), "omitted") {
		t.Fatalf("no omission marker:\n%s", stripANSI(last))
	}
}

// TestLiveThinkingRenders pins that unflushed deltas are drawn as the last block,
// and that the final trace event clears them so they are not shown twice.
func TestLiveThinkingRenders(t *testing.T) {
	events := []contract.Event{
		{ID: "r1", Type: "reason.response", WorkerID: "niq", Payload: map[string]any{"content": []any{"done"}}},
	}
	lines := renderTranscript(events, "", 40, "still thinking", "niq")
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "still thinking") {
		t.Fatalf("live thinking missing:\n%s", joined)
	}
	if !strings.Contains(joined, "thinking: still thinking") {
		t.Fatalf("live thinking not labelled:\n%s", joined)
	}

	// A filter for another worker hides it.
	if got := stripANSI(strings.Join(renderTranscript(events, "other", 40, "x", "niq"), "\n")); strings.Contains(got, "thinking") {
		t.Fatalf("live thinking leaked through a filter:\n%s", got)
	}

	// The chat clears the buffer when the final trace arrives.
	c := &chat{seen: map[string]bool{}, in: newInput()}
	c.addLive(contract.Event{Type: "reason.thinking_delta", WorkerID: "niq", Payload: map[string]any{"text": "hmm"}})
	if c.liveThinking != "hmm" {
		t.Fatalf("delta not buffered: %q", c.liveThinking)
	}
	c.addLive(contract.Event{ID: "t1", Type: "reason.thinking", WorkerID: "niq",
		Payload: map[string]any{"content": []any{"hmm, final"}}})
	if c.liveThinking != "" {
		t.Fatalf("live buffer not cleared: %q", c.liveThinking)
	}
}
