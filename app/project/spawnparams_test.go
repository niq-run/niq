package project

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/impl/workerhost"
	"github.com/niq-run/niq/core/itfs/worker"
)

// TestSpawnParamsCoverEveryBuilder is the invariant behind per-type validation:
// a type the service can build must also declare what its spawn accepts,
// otherwise a new builder would quietly ship without a check.
func TestSpawnParamsCoverEveryBuilder(t *testing.T) {
	svc := workerhost.New()
	RegisterBuilders(BuildContext{}, svc)

	for _, typ := range svc.RegisteredTypes() {
		if _, ok := spawnParamsByType[typ]; !ok {
			t.Errorf("builder type %q has no spawn-parameter contract in spawnParamsByType", typ)
		}
	}
	for typ := range spawnParamsByType {
		if !slices.Contains(svc.RegisteredTypes(), typ) {
			t.Errorf("spawnParamsByType declares %q, but no builder is registered for it", typ)
		}
	}
}

// TestCheckSpawnParamsAcceptsDocumentedPayloads walks the payloads the WebUI and
// the preset actually produce. Each carries the type's own keys plus the
// universal ones (publish/subscriptions are sent for every managed type).
func TestCheckSpawnParamsAcceptsDocumentedPayloads(t *testing.T) {
	cases := map[string]map[string]any{
		"reason": {
			"type": "reason", "id": "analyst",
			"instruction": "Watch the tracker.", "goal": "Summarize new issues.",
			"brief": "The labels are in the goal.", "readme": "issue triager",
			"programs": []any{map[string]any{"name": "p", "content_type": "playbook"}},
			"events":   []any{"github.issue.new"},
			"provider": "openai", "model": "gpt-4o",
			"collaborators":  []any{"reviewer"},
			"context_window": 32_000,
			"budget_soft":    0.85, "budget_hard": 0.97,
			"tail_tokens": 10_000, "keep_tail": 10,
			"rotate_rounds": 2, "compact_directive": "summarize tersely",
			"publish":       []any{"*"},
			"subscriptions": []any{map[string]any{"type": "worker.input", "source": "*"}},
			"label":         "analyst", "tags": []any{"ops/triage"}, "description": "triage",
		},
		"coding": {
			"type": "coding", "id": "dev", "instruction": "Write code.", "tool_mode": "ptc",
			"provider": "openai", "publish": []any{"*"},
		},
		"workspace": {
			"type": "workspace", "id": "ws",
			"mounts":   []any{"/tmp/a", "/tmp/b"},
			"approver": "webui-hiw",
			"publish":  []any{"request.*", "worker.ready", "approval.request"},
		},
		"program":   {"type": "program", "mounts": []any{"/tmp/programs"}},
		"timer":     {"type": "timer", "readme": "custom", "readme_append": "more"},
		"host":      {"type": "host"},
		"directory": {"type": "directory"},
		"hiw":       {"type": "hiw"},
		"history":   {"type": "history", "max_list_events": 10, "max_list_item_bytes": 1024},
		"mcp": {
			"type": "mcp", "expose": "agentic", "fixed": true,
			"servers":       []any{map[string]any{"name": "gh", "url": "https://x"}},
			"allowed_tools": []any{"search"},
		},
		"niw": {
			"type": "niw", "remote_url": "http://b", "remote_worker_id": "far",
			"remote_credential": "env:TOKEN", "recipient": "reason.local",
		},
	}
	for typ, payload := range cases {
		check := checkSpawnParams(typ, spawnParamsByType[typ])
		if err := check(payload); err != nil {
			t.Errorf("%s: documented payload rejected: %v", typ, err)
		}
	}
}

// TestCheckSpawnParamsRejectsUnreadParameters is the point of the change: a
// parameter the type does not read must fail the call, not be dropped.
func TestCheckSpawnParamsRejectsUnreadParameters(t *testing.T) {
	check := checkSpawnParams("reason", spawnParamsByType["reason"])
	err := check(map[string]any{
		"type": "reason", "id": "analyst", "prompt": "Triage issues.",
	})
	if err == nil {
		t.Fatal("reason accepted \"prompt\": it would look like a successful spawn that initialised nothing")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"prompt"`) {
		t.Fatalf("error does not name the offending key: %q", msg)
	}
	if !strings.Contains(msg, `did you mean "instruction"`) {
		t.Fatalf("error does not point at the reason-family instruction key: %q", msg)
	}
	if !strings.Contains(msg, "instruction") || !strings.Contains(msg, "goal") {
		t.Fatalf("error does not list what the type accepts: %q", msg)
	}
}

// TestCheckSpawnParamsRejectsCrossTypeKeys covers the same mistake between two
// known types: a reason-only key on a workspace.
func TestCheckSpawnParamsRejectsCrossTypeKeys(t *testing.T) {
	check := checkSpawnParams("workspace", spawnParamsByType["workspace"])
	err := check(map[string]any{"type": "workspace", "id": "ws", "goal": "Do things."})
	if err == nil || !strings.Contains(err.Error(), `"goal"`) {
		t.Fatalf("workspace err = %v, want it to reject \"goal\"", err)
	}
	// The alias hint must not fire when the suggested key is invalid for the type.
	if strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("misleading hint for workspace: %q", err)
	}
	// And the mounts/path contract is what it says it accepts.
	if !strings.Contains(err.Error(), "mounts") || !strings.Contains(err.Error(), "path") {
		t.Fatalf("error does not list the workspace contract: %q", err)
	}
}

// TestCheckSpawnParamsUniversalKeys verifies the keys every type accepts
// (identity, display metadata, ACL grants) are not rejected anywhere.
func TestCheckSpawnParamsUniversalKeys(t *testing.T) {
	universal := map[string]any{
		"type": "x", "id": "y", "label": "L", "tags": []any{"t"},
		"description": "d", "publish": []any{"*"}, "subscriptions": []any{"worker.input"},
	}
	for typ, params := range spawnParamsByType {
		payload := map[string]any{}
		for k, v := range universal {
			payload[k] = v
		}
		if err := checkSpawnParams(typ, params)(payload); err != nil {
			t.Errorf("%s rejected a universal key: %v", typ, err)
		}
	}
}

// TestSpawnRejectsUnknownParamsEndToEnd drives the real registration path: the
// host's CreateWorker call must fail on the check and never reach the builder.
func TestSpawnRejectsUnknownParamsEndToEnd(t *testing.T) {
	svc := workerhost.New()
	RegisterBuilders(BuildContext{}, svc)

	err := svc.CreateWorker(context.Background(), worker.WorkerConfig{
		ID: "analyst", Type: "reason",
		Params: map[string]any{"type": "reason", "id": "analyst", "prompt": "Triage issues."},
	})
	if err == nil {
		t.Fatal("CreateWorker accepted an unread spawn parameter")
	}
	if !strings.Contains(err.Error(), `"prompt"`) || !strings.Contains(err.Error(), "reason accepts") {
		t.Fatalf("err = %q, want the unknown-parameter error with the accepted list", err)
	}
	if svc.HasWorker("analyst") {
		t.Fatal("the rejected spawn still registered a worker")
	}
}
