// The picker's own wording and small formatting helpers: the version tag, the
// running/stopped word, bracketed labels, a home-relative path and a port cell.
// They are presentation, not layout, so they live with the picker that uses them.
package picker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/niq-run/niq/app/client/tui/term"
)

// Version is set by the CLI before it opens the picker, so the header shows the
// same version as `niq --version`.
var Version = "dev"

func versionTag() string {
	return term.Dim("version: ") + term.Brand(strings.TrimPrefix(Version, "v"))
}

func bracketed(name string) string {
	return term.BrandBold("[") + term.Bold(name) + term.BrandBold("]")
}

func stateWord(running bool) string {
	if running {
		return term.Green(fmt.Sprintf("%-7s", "running"))
	}
	return term.Dim(fmt.Sprintf("%-7s", "stopped"))
}

func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

func portCell(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}
