package host

import "testing"

// TestLiftDisplayMeta verifies the spawn arguments that are display metadata
// only (label / tags / description) are lifted onto typed fields and never
// reach the builder as construction params.
func TestLiftDisplayMeta(t *testing.T) {
	args := map[string]any{
		"type":        "reason",
		"id":          "oncall",
		"label":       "  值班机器人  ",
		"tags":        []any{"ops/backup", "work"},
		"description": "值班专用",
		"goal":        "watch the fleet",
	}

	label, tags, desc := liftDisplayMeta(args)
	if label != "值班机器人" {
		t.Fatalf("label = %q, want the trimmed display name", label)
	}
	if len(tags) != 2 || tags[0] != "ops/backup" || tags[1] != "work" {
		t.Fatalf("tags = %#v", tags)
	}
	if desc != "值班专用" {
		t.Fatalf("description = %q", desc)
	}
	for _, k := range []string{"label", "tags", "description"} {
		if _, ok := args[k]; ok {
			t.Fatalf("%q leaked into the construction params: %+v", k, args)
		}
	}
	if args["type"] != "reason" || args["goal"] != "watch the fleet" {
		t.Fatalf("construction params were damaged: %+v", args)
	}
}
