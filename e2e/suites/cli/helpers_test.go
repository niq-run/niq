//go:build e2e

package cli_e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/e2e/harness"
)

// decodeStatus runs `niq status --json` and decodes it. It works with or
// without a running gateway: a stopped plane is part of the contract.
func decodeStatus(t *testing.T, s *harness.Sandbox) harness.Status {
	t.Helper()
	res := s.Run("status", "--json").RequireExit(t, 0)
	var st harness.Status
	if err := json.Unmarshal([]byte(res.Stdout), &st); err != nil {
		t.Fatalf("decode status --json: %v\n%s", err, res)
	}
	return st
}

// columnOf is the display column where needle starts in line. Rows are compared
// by cells, not bytes: the table pads ids by their rendered width, so a CJK id
// and an ASCII one of the same width must start the state column together.
func columnOf(t *testing.T, line, needle string) int {
	t.Helper()
	i := strings.Index(line, needle)
	if i < 0 {
		t.Fatalf("line %q has no %q", line, needle)
	}
	return displayWidth(line[:i])
}

// displayWidth counts terminal cells for the ids used here: East Asian wide
// runes take two, everything else one. Colours are off in a pipe, so a row is
// plain text.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r >= 0x2E80 && r <= 0xA4CF, // CJK radicals, kana, han, Yi
			r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
			r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
			r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
			r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
			r >= 0x20000 && r <= 0x3FFFD:
			w += 2
		default:
			w++
		}
	}
	return w
}

// rowFor returns the line of a table that describes id, or fails.
func rowFor(t *testing.T, output, id string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "["+id+"]") {
			return line
		}
	}
	t.Fatalf("no row for %q in:\n%s", id, output)
	return ""
}

// fileExists reports whether path is present.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// getStatus performs a GET against an arbitrary host:port and returns the code.
// It is how a test talks to a project's own data port, which speaks no HTML.
func getStatus(t *testing.T, addr, path string) int {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + path)
	if err != nil {
		t.Fatalf("GET %s%s: %v", addr, path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// readPath reads an absolute path outside the sandbox (the completion target
// lives under HOME, which the harness does not expose by relative path).
func readPath(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}
