package quickjsexec

import (
	"regexp"
	"strconv"
	"strings"

	"modernc.org/quickjs"
)

// evalFrame matches one QuickJS stack frame location, e.g. "at <eval>:12:5".
// The engine names the compiled script "<eval>"; the prelude's lines are
// subtracted from it so the caller sees a line of its own source.
var evalFrame = regexp.MustCompile(`<eval>:(\d+):(\d+)`)

// locate renders an engine error with the script's own line: a parse error
// carries the location itself, a runtime error only in its stack.
func locate(err *quickjs.Error, offset int, src string) string {
	if err.LineNumber > 0 {
		return withLine(err.Name, err.Message, err.LineNumber-offset, err.ColumnNumber, src)
	}
	return describe(err.Name+": "+err.Message+"\n"+err.Stack, offset, src)
}

// describe locates an error the script formatted itself ("TypeError: x" and a
// stack, or just a message). The first frame that resolves to a line of the
// script wins; the wrapper's own frames fall outside that range and are skipped.
func describe(raw string, offset int, src string) string {
	headline, frames, _ := strings.Cut(raw, "\n")
	if frames == "" {
		return strings.TrimSpace(headline)
	}
	lines := strings.Split(src, "\n")
	for _, frame := range strings.Split(frames, "\n") {
		m := evalFrame.FindStringSubmatch(frame)
		if m == nil {
			continue
		}
		line, _ := strconv.Atoi(m[1])
		column, _ := strconv.Atoi(m[2])
		if user := line - offset; user >= 1 && user <= len(lines) {
			return withLine("", headline, user, column, src)
		}
	}
	return strings.TrimSpace(headline)
}

// withLine appends the location and the offending source line. name is empty
// when headline already carries "Name: message".
func withLine(name, headline string, line, column int, src string) string {
	if name != "" {
		headline = name + ": " + headline
	}
	headline = strings.TrimSpace(headline)
	if line <= 0 {
		return headline
	}
	out := headline + "\n    at script line " + strconv.Itoa(line) + ":" + strconv.Itoa(column)
	lines := strings.Split(src, "\n")
	if line <= len(lines) {
		if text := strings.TrimSpace(lines[line-1]); text != "" {
			out += ": " + text
		}
	}
	return out
}
