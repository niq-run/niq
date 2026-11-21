//go:build e2e

package cli_e2e

import (
	"os"
	"testing"

	"github.com/niq-run/niq/e2e/harness"
)

// TestMain builds the CLI once for the whole suite.
func TestMain(m *testing.M) { os.Exit(harness.Main(m)) }
