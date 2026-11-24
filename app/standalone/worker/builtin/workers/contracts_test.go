package workers

import (
	"testing"

	"github.com/niq-run/niq/app/standalone/worker/builtin"
)

// contractOf returns the spawn contract a type registered with — the same list
// its check was built from — so a test asserts against the registration rather
// than a copy of it.
func contractOf(t *testing.T, typ string) []string {
	t.Helper()
	keys, ok := allContracts(t)[typ]
	if !ok {
		t.Fatalf("type %q registered no spawn contract", typ)
	}
	return keys
}

// allContracts is every registered type's contract, keyed by type.
func allContracts(t *testing.T) map[string][]string {
	t.Helper()
	reg := builtin.NewRegistry()
	Register(builtin.BuildContext{}, reg) // the builders are never run: only the table is built
	out := make(map[string][]string, len(reg.Types()))
	for _, typ := range reg.Types() {
		if keys, ok := reg.Accepted(typ); ok {
			out[typ] = keys
		}
	}
	return out
}
