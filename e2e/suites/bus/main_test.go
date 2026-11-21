//go:build e2e

package bus_e2e

import (
	"os"
	"testing"

	"github.com/niq-run/niq/e2e/harness"
)

func TestMain(m *testing.M) { os.Exit(harness.Main(m)) }
