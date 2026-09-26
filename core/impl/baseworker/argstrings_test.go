package baseworker

import (
	"reflect"
	"testing"
)

func TestArgStrings(t *testing.T) {
	args := map[string]any{
		"tags":      []any{"ops/backup", " work "},
		"strs":      []string{"a", "b"},
		"csv":       "  alpha , beta ,,gamma ",
		"single":    "only",
		"emptyComma": ", ,",
		"emptyStr":  "   ",
		"num":       float64(5),
	}
	for _, tc := range []struct {
		key   string
		want  []string
	}{
		{"tags", []string{"ops/backup", "work"}},
		{"strs", []string{"a", "b"}},
		{"csv", []string{"alpha", "beta", "gamma"}},
		{"single", []string{"only"}},
		{"emptyComma", nil},
		{"emptyStr", nil},
		{"num", nil},
		{"missing", nil},
	} {
		if got := ArgStrings(args, tc.key); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ArgStrings(%q) = %#v, want %#v", tc.key, got, tc.want)
		}
	}
}

func TestDeclFromSpawnMeta(t *testing.T) {
	// Ensured via project package; kept here to document ArgStrings feeds tags.
	if got := ArgStrings(map[string]any{"tags": []any{"work", "ops"}}, "tags"); len(got) != 2 {
		t.Fatalf("expected 2 tags, got %#v", got)
	}
}