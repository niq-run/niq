package workers

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/worker/builtin"
)

// TestCheckSpawnParamsAcceptsDocumentedPayloads walks the payloads the WebUI and
// the preset actually produce. Each carries the type's own keys plus the
// universal ones (publish/subscriptions are sent for every managed type).
func TestCheckSpawnParamsAcceptsDocumentedPayloads(t *testing.T) {
	cases := map[string]map[string]any{
		"reason": {
			"instruction": "Watch the tracker.", "goal": "Summarize new issues.",
			"brief": "The labels are in the goal.", "readme": "issue triager",
			"programs": []any{map[string]any{"name": "p", "content_type": "playbook"}},
			"events":   []any{"github.issue.new"},
			"provider": "openai", "model": "gpt-4o",
			"collaborators": []any{"reviewer"},
			"budget_soft":   0.85, "budget_hard": 0.97,
			"tail_tokens": 10_000, "keep_tail": 10,
			"compact_directive": "summarize tersely",
		},
		"coding": {
			"instruction": "Write code.", "tool_mode": "ptc", "provider": "openai",
		},
		"workspace": {
			"mounts": []any{"/tmp/a", "/tmp/b"}, "approver": "webui",
		},
		"program":   {"mounts": []any{"/tmp/programs"}},
		"timer":     {"readme": "custom", "readme_append": "more"},
		"host":      {},
		"directory": {},
		"history":   {"max_list_events": 10, "max_list_item_bytes": 1024},
		"mcp": {
			"expose": "agentic", "fixed": true,
			"servers":       []any{map[string]any{"name": "gh", "url": "https://x"}},
			"allowed_tools": []any{"search"},
		},
		"niq": {
			"remote_url": "http://b", "remote_worker_id": "far",
			"remote_credential": "env:TOKEN", "recipient": "reason.local",
		},
	}
	for typ, payload := range cases {
		if err := builtin.CheckSpawnParams(typ, contractOf(t, typ))(payload); err != nil {
			t.Errorf("%s: documented payload rejected: %v", typ, err)
		}
	}
}

// TestCheckSpawnParamsRejectsUnreadParameters is the point of the change: a
// parameter the type does not read must fail the call, not be dropped.
func TestCheckSpawnParamsRejectsUnreadParameters(t *testing.T) {
	check := builtin.CheckSpawnParams("reason", contractOf(t, "reason"))
	err := check(map[string]any{"prompt": "Triage issues."})
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
	check := builtin.CheckSpawnParams("workspace", contractOf(t, "workspace"))
	err := check(map[string]any{"goal": "Do things."})
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

// TestCheckSpawnParamsRejectsIdentityKeys verifies identity, display and ACL
// fields are NOT params: they live on the declaration, so a caller that puts
// them inside params gets a clear error rather than a silently duplicated value.
func TestCheckSpawnParamsRejectsIdentityKeys(t *testing.T) {
	bad := map[string]any{
		"type": "x", "id": "y", "label": "L", "tags": []any{"t"},
		"description": "d", "publish": []any{"*"}, "subscriptions": []any{"worker.input"},
	}
	for typ, params := range allContracts(t) {
		for k, v := range bad {
			if err := builtin.CheckSpawnParams(typ, params)(map[string]any{k: v}); err == nil {
				t.Errorf("%s accepted identity/ACL key %q in params", typ, k)
			}
		}
	}
}

// TestValidateProcessParams verifies managed-process params are env-shaped.
func TestValidateProcessParams(t *testing.T) {
	if err := builtin.ValidateProcessParams(map[string]any{"TOKEN": "env:TOKEN", "ROOT": "/data"}); err != nil {
		t.Fatalf("valid env rejected: %v", err)
	}
	for _, bad := range []map[string]any{
		{"K": 1},
		{"K": []any{"x"}},
		{"NIQ_WORKER_ID": "x"},
		{"A=B": "x"},
	} {
		if err := builtin.ValidateProcessParams(bad); err == nil {
			t.Fatalf("invalid env accepted: %+v", bad)
		}
	}
}
