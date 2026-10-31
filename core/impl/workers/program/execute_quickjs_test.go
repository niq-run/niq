package program

import (
	"context"
	"testing"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/workers/program/pgbackend"
	"github.com/niq-run/niq/core/impl/workers/program/quickjsexec"
	"github.com/niq-run/niq/core/itfs/program"
)

// TestHandleRunOnQuickJS is the integration seam the fake executors cannot
// cover: a real program.run through the worker's reply shaping on the real
// executor, including a structured output and the script log.
func TestHandleRunOnQuickJS(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{
		ID:      "program",
		Bus:     ch,
		Backend: program.Compose(pgbackend.New(t.TempDir()), quickjsexec.New(quickjsexec.Options{})),
	})

	go w.handleRun(context.Background(), baseworker.ToolCall{
		CallerID: "coder", CallID: "call-qjs", TraceID: "tr",
		Args: map[string]any{"source": `sdk.log("step 1"); return { n: 2, list: ["a", "b"] }`},
	})

	waitUntil(t, 10*time.Second, func() bool {
		_, ok := replyFor(t, ch, "call-qjs")
		return ok
	}, "program.run reply from quickjs")

	out, lines, ok := runOutput(t, ch, "call-qjs")
	if !ok {
		t.Fatal("no reply")
	}
	obj, isObj := out.(map[string]any)
	if !isObj {
		t.Fatalf("output = %#v, want the script's object", out)
	}
	if obj["n"] != float64(2) {
		t.Fatalf("output.n = %#v, want 2", obj["n"])
	}
	if len(lines) != 1 || lines[0] != "step 1" {
		t.Fatalf("log = %#v, want [step 1]", lines)
	}
}
