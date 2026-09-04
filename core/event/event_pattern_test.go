package event

import (
	"encoding/json"
	"testing"
)

func TestEventPatternJSONObjectForm(t *testing.T) {
	// The legacy spelling (pre-unification tag, still in persisted files)
	// parses; the source restriction must not silently drop.
	in := `{"type":"pr.ready","source_id":"gh"}`
	var p EventPattern
	if err := json.Unmarshal([]byte(in), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Type != "pr.ready" || p.SourceID != "gh" {
		t.Fatalf("got %+v", p)
	}
	// Round-trip emits the canonical tag.
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"type":"pr.ready","source":"gh"}`; string(out) != want {
		t.Fatalf("round-trip = %s, want %s", out, want)
	}
}

func TestEventPatternMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern EventPattern
		typ     string
		source  string
		want    bool
	}{
		{"wildcard", EventPattern{Type: "*"}, "anything", "", true},
		{"exact", EventPattern{Type: "request.completed"}, "request.completed", "", true},
		{"exact-miss", EventPattern{Type: "request.completed"}, "request.failed", "", false},
		{"prefix", EventPattern{Type: "github.*"}, "github.issue.new", "", true},
		{"prefix-miss", EventPattern{Type: "github.*"}, "gitlab.issue", "", false},
		{"empty-miss", EventPattern{}, "anything", "", false},
		{"source-match", EventPattern{Type: "pr.ready", SourceID: "gh"}, "pr.ready", "gh", true},
		{"source-miss", EventPattern{Type: "pr.ready", SourceID: "gh"}, "pr.ready", "gitlab", false},
		{"source-wildcard", EventPattern{Type: "*", SourceID: "gh"}, "pr.ready", "gh", true},
	}
	for _, c := range cases {
		evt := Event{Type: EventType(c.typ), WorkerId: c.source}
		if got := c.pattern.Matches(evt); got != c.want {
			t.Errorf("%s: Match() = %v, want %v", c.name, got, c.want)
		}
	}
}
