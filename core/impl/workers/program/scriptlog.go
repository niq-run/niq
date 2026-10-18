package program

import (
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Script-log return policy. A run's sdk.log lines are returned to the CALLER
// alongside the result, because for a PTC round the caller is the model: seeing
// its own script's log is what lets it debug a failing run inside the same
// round instead of asking for a second one. The text is capped — the tail is
// what explains a failure, and an unbounded log would blow up the reply payload
// and the caller's context.
const (
	scriptLogMaxLines = 100
	scriptLogMaxBytes = 8 * 1024

	// scriptResultMaxBytes caps the RESULT text handed back to the caller, in the
	// same spirit (and roughly the same size) as the workspace tools' 20KB
	// output cap: a run that returns a huge blob must not silently blow up the
	// caller's context. Head and tail are kept, the middle is elided, so both
	// the beginning and the ending of the payload stay readable.
	scriptResultMaxBytes = 20 * 1024
)

// scriptLog collects a run's sdk.log lines for the reply. It is written by the
// session's log-draining goroutine and read after the session ends, so access
// is guarded.
type scriptLog struct {
	mu      sync.Mutex
	lines   []string
	bytes   int
	dropped int
}

// add appends one log line, dropping the OLDEST lines once the caps are hit so
// the retained window is the end of the run.
func (l *scriptLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line = strings.TrimRight(line, "\n")
	l.lines = append(l.lines, line)
	l.bytes += len(line) + 1
	for len(l.lines) > scriptLogMaxLines || (l.bytes > scriptLogMaxBytes && len(l.lines) > 1) {
		l.bytes -= len(l.lines[0]) + 1
		l.lines = l.lines[1:]
		l.dropped++
	}
}

// text renders the collected lines as the reply's log section, or "" when the
// script logged nothing (so a quiet run's reply is exactly its result).
func (l *scriptLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	var b strings.Builder
	if l.dropped > 0 {
		b.WriteString("[" + strconv.Itoa(l.dropped) + " earlier line(s) dropped]\n")
	}
	b.WriteString(strings.Join(l.lines, "\n"))
	return b.String()
}

// withScriptLog appends the run's log section to the reply text. The result
// stays first and untouched, so a caller that only reads the head is unaffected.
func withScriptLog(result string, logs *scriptLog) string {
	text := logs.text()
	if text == "" {
		return result
	}
	const header = "\n\n--- script log ---\n"
	if result == "" {
		return strings.TrimPrefix(header, "\n\n") + text
	}
	return result + header + text
}

// capScriptResult keeps a result within scriptResultMaxBytes by eliding its
// middle. The note states how much was dropped, so the caller can tell a
// truncated payload from a short one.
func capScriptResult(result string) string {
	if len(result) <= scriptResultMaxBytes {
		return result
	}
	head := scriptResultMaxBytes / 2
	tail := scriptResultMaxBytes - head
	// Cut on rune boundaries so the kept text stays valid UTF-8.
	headEnd := head
	for headEnd > 0 && !utf8.RuneStart(result[headEnd]) {
		headEnd--
	}
	tailStart := len(result) - tail
	for tailStart < len(result) && !utf8.RuneStart(result[tailStart]) {
		tailStart++
	}
	elided := tailStart - headEnd
	return result[:headEnd] +
		"\n\n[... " + strconv.Itoa(elided) + " bytes elided ...]\n\n" +
		result[tailStart:]
}
