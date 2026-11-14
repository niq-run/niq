// Tests for the program attribute: the runtime attach/detach contract, the
// single-attribute restore rule, and the prompt-cache ledger — the system prompt
// renders only the applied set, so an operation reaches it at a cache-reset
// boundary instead of mid-episode. These are the invariants the prompt-cache
// optimization rests on.
package reason

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/impl/reason/transcript"
	llm "github.com/niq-run/niq/core/itfs/llm"
	"github.com/niq-run/niq/core/itfs/program"
)

// TestProgramAttachDetach exercises the runtime program mutations: pure-add
// semantics (duplicate name rejected), locked protection on detach, and
// not-found errors.
func TestProgramAttachDetach(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)

	if _, err := w.AttachProgram(program.Program{
		Meta:         program.Meta{Name: "p1", ContentType: "instruction"},
		EntryContent: program.ProgramContent{Content: "x"},
	}); err != nil {
		t.Fatalf("attach p1: %v", err)
	}
	if _, err := w.AttachProgram(program.Program{Meta: program.Meta{Name: "p1", ContentType: "instruction"}}); err == nil {
		t.Fatal("duplicate attach should fail")
	}
	if _, err := w.DetachProgram("nope"); err == nil {
		t.Fatal("detach of missing program should fail")
	}

	// Locked programs may be attached but not detached via the meta-extension.
	if _, err := w.AttachProgram(program.Program{Meta: program.Meta{Name: "locked", ContentType: "playbook", Locked: true}}); err != nil {
		t.Fatalf("attach locked: %v", err)
	}
	if _, err := w.DetachProgram("locked"); err == nil {
		t.Fatal("detach of locked program should fail")
	}

	if _, err := w.DetachProgram("p1"); err != nil {
		t.Fatalf("detach p1: %v", err)
	}
	got := w.Programs()
	if len(got) != 1 || got[0].Name != "locked" {
		t.Fatalf("after ops: %+v", got)
	}
}

// TestProgramSnapshotRestoreOverrides verifies the single-attribute contract:
// once a worker has a snapshot, restore unconditionally overrides config, so a
// config-only program is dropped in favour of the snapshot's list.
func TestProgramSnapshotRestoreOverrides(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.programs = programState{Applied: []program.Program{
		{Meta: program.Meta{Name: "cfg-only", ContentType: "instruction"}, EntryContent: program.ProgramContent{Content: "c"}},
	}}
	if _, err := w.AttachProgram(program.Program{Meta: program.Meta{Name: "runtime", ContentType: "playbook"}}); err != nil {
		t.Fatal(err)
	}
	snap, err := w.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	w2 := newTestWorker(&staticProvider{}, nil)
	w2.programs = programState{Applied: []program.Program{
		{Meta: program.Meta{Name: "other-cfg", ContentType: "instruction"}, EntryContent: program.ProgramContent{Content: "o"}},
	}}
	if err := w2.Restore(snap); err != nil {
		t.Fatal(err)
	}

	got := w2.Programs()
	if len(got) != 2 {
		t.Fatalf("restore should override config; got %d programs: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name] = true
	}
	if !names["cfg-only"] || !names["runtime"] {
		t.Fatalf("restore lost a snapshot program: %+v", got)
	}
	if names["other-cfg"] {
		t.Fatalf("restore did not override config: %+v", got)
	}
}

// TestProgramRestoreKeepsConfigWhenAbsent verifies backward compat: a snapshot
// taken before the Programs field existed (field absent → nil) leaves config
// standing rather than wiping it.
func TestProgramRestoreKeepsConfigWhenAbsent(t *testing.T) {
	w0 := newTestWorker(&staticProvider{}, nil)
	snap0, err := w0.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	// Strip the program attribute, simulating a pre-change snapshot.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(snap0, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "program_state")
	stripped, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	w := newTestWorker(&staticProvider{}, nil)
	w.programs = programState{Applied: []program.Program{
		{Meta: program.Meta{Name: "cfg", ContentType: "instruction"}, EntryContent: program.ProgramContent{Content: "c"}},
	}}
	if err := w.Restore(stripped); err != nil {
		t.Fatal(err)
	}
	got := w.Programs()
	if len(got) != 1 || got[0].Name != "cfg" {
		t.Fatalf("old snapshot should keep config: %+v", got)
	}
}

// ── The prompt-cache ledger ──────────────────────────────────────────────────
//
// An operation is recorded in the ledger and becomes effective immediately, but
// the applied set — the only input of the system prompt — only follows at a
// cache-reset boundary (a compaction, a provider switch, or the first request).

func instrProgram(name, content string) program.Program {
	return program.Program{
		Meta:         program.Meta{Name: name, ContentType: program.ContentTypeInstruction},
		Path:         name,
		EntryContent: program.ProgramContent{Name: name, FormType: program.FormTypePrompt, Path: name, Content: content},
	}
}

func playbookProgram(name, desc string) program.Program {
	return program.Program{
		Meta: program.Meta{Name: name, ContentType: program.ContentTypePlaybook, Description: desc},
		Path: name,
	}
}

// mustAttachProgram records an attach through the handler-side entry point
// (which requires w.mu held).
func mustAttachProgram(t *testing.T, w *BaseReasonWorker, p program.Program) ProgramOp {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	op, err := w.AttachProgramLocked(p)
	if err != nil {
		t.Fatalf("attach %q: %v", p.Name, err)
	}
	return op
}

func mustDetachProgram(t *testing.T, w *BaseReasonWorker, name string) ProgramOp {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	op, err := w.DetachProgramLocked(name)
	if err != nil {
		t.Fatalf("detach %q: %v", name, err)
	}
	return op
}

func programNames(list []program.Program) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.Name)
	}
	return out
}

// TestCommitTranscriptArmsOnlyOnRewrite pins the boundary criterion: it is read
// from the edit itself (a head is the transcript's new front), not declared by
// the caller. A compression that folds nothing must therefore be harmless —
// were the caller's own "rewrote" verdict trusted instead, one wrong boolean
// would re-render the prompt and throw away a warm prefix cache.
func TestCommitTranscriptArmsOnlyOnRewrite(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.promptSent = true
	w.transcript.Apply(transcript.InputPatch{Messages: []llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}},
	}}})
	mustAttachProgram(t, w, instrProgram("extra", "EXTRA RULE"))

	// No-op compression: an edit that commits the same transcript back, with no
	// head. The edit is ended (and buffered inputs merged), but nothing is armed.
	msgs := w.transcript.BeginEdit()
	w.CommitTranscript(nil, msgs)
	if w.programs.ApplyPending {
		t.Fatal("a commit with no head must not arm the ledger")
	}
	if got := w.buildInstruction(); strings.Contains(got, "EXTRA RULE") {
		t.Fatalf("prompt must stay unchanged after a no-op commit:\n%s", got)
	}

	// Real compression: the digest replaces the front, so the commit arms it.
	w.transcript.BeginEdit()
	w.CommitTranscript([]llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "[context digest] older history"}},
	}}, nil)
	if !w.programs.ApplyPending {
		t.Fatal("a commit that installs a new head must arm the ledger")
	}
	_, req := w.prepareReasoning()
	if !strings.Contains(req.Context.SystemPrompt, "EXTRA RULE") {
		t.Fatalf("the round after a rewrite should render the ledger:\n%s", req.Context.SystemPrompt)
	}
}

// TestProgramLedgerDefersApply is the core contract: an operation is recorded,
// becomes effective immediately (the effective view and its checks see it), but
// the applied set — the only input of the system prompt — does not change.
func TestProgramLedgerDefersApply(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.programs.Applied = []program.Program{instrProgram("base", "BASE RULE")}

	before := w.buildInstruction()
	mustAttachProgram(t, w, instrProgram("extra", "EXTRA RULE"))
	mustAttachProgram(t, w, playbookProgram("book", "a procedure"))

	if got := w.buildInstruction(); got != before {
		t.Fatalf("system prompt changed with a pending ledger:\n%s", got)
	}
	if got := programNames(w.Programs()); len(got) != 3 || got[0] != "base" || got[1] != "extra" || got[2] != "book" {
		t.Fatalf("effective view should carry pending operations, got %v", got)
	}
	if len(w.programs.Delta) != 2 {
		t.Fatalf("ledger should hold 2 operations, got %d", len(w.programs.Delta))
	}
	if len(w.programs.Applied) != 1 {
		t.Fatalf("applied set must not change before an boundary, got %v", programNames(w.programs.Applied))
	}
}

// TestProgramLedgerFoldOrder pins the replay semantics: apply folds in issue
// order, so detach-then-attach is a replacement and attach-then-detach cancels.
func TestProgramLedgerFoldOrder(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.programs.Applied = []program.Program{instrProgram("base", "BASE RULE")}

	// Cancel a pending attach: attach then detach before any apply.
	mustAttachProgram(t, w, instrProgram("temp", "TEMP RULE"))
	mustDetachProgram(t, w, "temp")
	if ops := w.programs.Apply(); len(ops) != 2 {
		t.Fatalf("apply should report the 2 settled operations, got %d", len(ops))
	}
	if got := programNames(w.programs.Applied); len(got) != 1 || got[0] != "base" {
		t.Fatalf("attach+detach should cancel out, got %v", got)
	}

	// Replace an applied entry: detach then attach again.
	mustDetachProgram(t, w, "base")
	mustAttachProgram(t, w, instrProgram("base", "REPLACED RULE"))
	w.programs.Apply()
	if len(w.programs.Applied) != 1 || w.programs.Applied[0].EntryContent.Content != "REPLACED RULE" {
		t.Fatalf("detach+attach should replace in place, got %+v", w.programs.Applied)
	}
}

// TestProgramLedgerChecksRunOnEffectiveView keeps attach/detach's safety rules
// unchanged even while the operation is still pending.
func TestProgramLedgerChecksRunOnEffectiveView(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)

	mustAttachProgram(t, w, instrProgram("p1", "x"))
	// A pending attach already counts as present: attach stays purely additive.
	if _, err := w.AttachProgram(instrProgram("p1", "y")); err == nil {
		t.Fatal("duplicate attach should fail against the pending ledger")
	}
	// A locked program is protected from the moment it is recorded.
	locked := instrProgram("locked", "y")
	locked.Locked = true
	mustAttachProgram(t, w, locked)
	if _, err := w.DetachProgram("locked"); err == nil {
		t.Fatal("a pending locked program should not be detachable")
	}
}

// TestPromptFoldsLedgerAtResetBoundary covers the settlement: after a compaction
// arms it, the very next round renders the new applied set and consumes both the
// ledger and the arm.
func TestPromptFoldsLedgerAtResetBoundary(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.promptSent = true // a prompt is already cached at the provider
	w.programs.Applied = []program.Program{instrProgram("base", "BASE RULE")}
	mustAttachProgram(t, w, instrProgram("extra", "EXTRA RULE"))

	if got := w.buildInstruction(); strings.Contains(got, "EXTRA RULE") {
		t.Fatalf("pending program must not be in the prompt yet:\n%s", got)
	}

	w.ApplyProgramsAtNextRound()
	_, req := w.prepareReasoning()
	if !strings.Contains(req.Context.SystemPrompt, "EXTRA RULE") {
		t.Fatalf("boundary should fold the ledger into the prompt:\n%s", req.Context.SystemPrompt)
	}
	if w.programs.ApplyPending {
		t.Fatal("the arm should be consumed by the fold")
	}
	if len(w.programs.Delta) != 0 {
		t.Fatalf("ledger should be settled, got %d operations", len(w.programs.Delta))
	}
	// The prompt is byte-stable for later rounds.
	_, req2 := w.prepareReasoning()
	if req2.Context.SystemPrompt != req.Context.SystemPrompt {
		t.Fatal("system prompt must stay byte-identical between rounds")
	}
}

// TestPromptKeepsLedgerWithoutBoundary is the negative half: rounds alone never
// fold a pending operation, because that would change the head of the request.
func TestPromptKeepsLedgerWithoutBoundary(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.promptSent = true
	mustAttachProgram(t, w, instrProgram("extra", "EXTRA RULE"))

	for i := 0; i < 3; i++ {
		_, req := w.prepareReasoning()
		if strings.Contains(req.Context.SystemPrompt, "EXTRA RULE") {
			t.Fatalf("round %d folded the ledger without a reset boundary", i)
		}
	}
	if len(w.programs.Delta) != 1 {
		t.Fatal("the ledger should still be pending")
	}
}

// TestPromptFoldsBeforeFirstRequest: with no cached prefix there is nothing to
// protect, so the first request simply renders everything.
func TestPromptFoldsBeforeFirstRequest(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	mustAttachProgram(t, w, instrProgram("extra", "EXTRA RULE"))

	_, req := w.prepareReasoning()
	if !strings.Contains(req.Context.SystemPrompt, "EXTRA RULE") {
		t.Fatalf("the first request should render the ledger:\n%s", req.Context.SystemPrompt)
	}
	if len(w.programs.Delta) != 0 {
		t.Fatal("the first request should settle the ledger")
	}
}

// TestProgramLedgerAppliesOnProviderSwitch: a switch starts on a cold cache, so
// the ledger is settled immediately rather than waiting for a compaction.
func TestProgramLedgerAppliesOnProviderSwitch(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.promptSent = true
	mustAttachProgram(t, w, instrProgram("extra", "EXTRA RULE"))

	w.mu.Lock()
	w.applyProgramsLocked()
	w.mu.Unlock()

	if got := programNames(w.programs.Applied); len(got) != 1 || got[0] != "extra" {
		t.Fatalf("provider switch should settle the ledger, applied = %v", got)
	}
}

// TestProgramLedgerSurvivesSnapshot: the debt is persisted with the worker, so a
// restart still folds at the boundary rather than losing the change.
func TestProgramLedgerSurvivesSnapshot(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.promptSent = true
	w.programs.Applied = []program.Program{instrProgram("base", "BASE RULE")}
	mustAttachProgram(t, w, instrProgram("extra", "EXTRA RULE"))
	w.ApplyProgramsAtNextRound()

	snap, err := w.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	restored := newTestWorker(&staticProvider{}, nil)
	if err := restored.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if len(restored.programs.Delta) != 1 || !restored.programs.ApplyPending {
		t.Fatalf("pending ledger + arm should survive the snapshot, got delta=%d arm=%v",
			len(restored.programs.Delta), restored.programs.ApplyPending)
	}
	if got := programNames(restored.programs.Applied); len(got) != 1 || got[0] != "base" {
		t.Fatalf("applied set should still be the pre-change one, got %v", got)
	}
	// The first round after the restart settles what the compaction armed.
	_, req := restored.prepareReasoning()
	if !strings.Contains(req.Context.SystemPrompt, "EXTRA RULE") {
		t.Fatalf("restored arm should fold on the next round:\n%s", req.Context.SystemPrompt)
	}
}

// TestProgramRestoreReadsLegacyList covers backward compatibility: a snapshot
// from before the ledger existed carries a bare programs list, which is the
// applied set outright.
func TestProgramRestoreReadsLegacyList(t *testing.T) {
	w0 := newTestWorker(&staticProvider{}, nil)
	snap0, err := w0.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(snap0, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "program_state")
	legacy, err := json.Marshal([]program.Program{instrProgram("legacy", "LEGACY RULE")})
	if err != nil {
		t.Fatal(err)
	}
	m["programs"] = legacy
	oldSnap, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	w := newTestWorker(&staticProvider{}, nil)
	if err := w.Restore(oldSnap); err != nil {
		t.Fatal(err)
	}
	if got := programNames(w.programs.Applied); len(got) != 1 || got[0] != "legacy" {
		t.Fatalf("legacy list should restore as the applied set, got %v", got)
	}
	if len(w.programs.Delta) != 0 {
		t.Fatalf("legacy snapshot implies no pending ledger, got %d", len(w.programs.Delta))
	}
	if got := w.buildInstruction(); !strings.Contains(got, "LEGACY RULE") {
		t.Fatalf("legacy list should render immediately:\n%s", got)
	}
}
