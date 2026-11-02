// Updates: niq is distributed as an npm package, so the updater is npm. Two
// entry points share the same check — `niq update`, which installs, and a
// silent one behind a bare `niq`, which only mentions that a newer release
// exists.
//
// The passive check is deliberately timid: it runs at most once a day, is
// bounded by a short timeout, is skipped when stdout is not a terminal, and
// never reports its own failures. A package manager is the authority on what is
// published, but a missing network is not the user's problem to solve right
// now.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/niq-run/niq/app/niqhome"
)

const (
	npmPackage = "@niq.run/niq"
	// The scoped name has to be escaped in the registry path; the unescaped
	// form 404s for some proxies.
	npmLatestURL = "https://registry.npmjs.org/@niq.run%2Fniq/latest"

	updateCheckTTL = 24 * time.Hour
	// Long enough for a cold TLS handshake to the registry; once a day, at the
	// end of the output, so it is never in the way.
	passiveCheckTimeout = 2 * time.Second
)

// updateCache remembers the last check, so a day of `niq` invocations costs one
// request rather than one each.
type updateCache struct {
	Checked string `json:"checked"` // RFC3339
	Latest  string `json:"latest"`
}

func updateCachePath() string { return filepath.Join(niqhome.Root(), "update-check.json") }

// latestRelease asks the registry which version is published.
func latestRelease(timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(npmLatestURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry answered %s", resp.Status)
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", err
	}
	if _, ok := semverParts(doc.Version); !ok {
		return "", fmt.Errorf("registry returned an unusable version %q", doc.Version)
	}
	return doc.Version, nil
}

// semverParts reads the numeric prefix of a version: "v0.14.1-18-gabc" →
// [0 14 1]. The git-describe suffix is not comparable across builds and is
// dropped — only the release number decides whether an update exists.
func semverParts(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	fields := strings.Split(v, ".")
	if len(fields) == 0 || fields[0] == "" {
		return out, false
	}
	for i := 0; i < len(fields) && i < 3; i++ {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// newerRelease reports whether latest is a later release than current.
func newerRelease(latest, current string) bool {
	l, ok := semverParts(latest)
	if !ok {
		return false
	}
	c, ok := semverParts(current)
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func readUpdateCache() (updateCache, bool) {
	b, err := os.ReadFile(updateCachePath())
	if err != nil {
		return updateCache{}, false
	}
	var c updateCache
	if err := json.Unmarshal(b, &c); err != nil {
		return updateCache{}, false
	}
	return c, true
}

func writeUpdateCache(latest string) {
	c := updateCache{Checked: time.Now().Format(time.RFC3339), Latest: latest}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := os.MkdirAll(niqhome.Root(), 0755); err != nil {
		return
	}
	_ = os.WriteFile(updateCachePath(), append(b, '\n'), 0644)
}

// cachedLatest returns the newest known published version, asking the registry
// only when the cache is older than a day. Failures are returned so `niq
// update` can report them; the passive path ignores them.
func cachedLatest(timeout time.Duration) (string, error) {
	if c, ok := readUpdateCache(); ok {
		if t, err := time.Parse(time.RFC3339, c.Checked); err == nil && time.Since(t) < updateCheckTTL {
			return c.Latest, nil
		}
	}
	latest, err := latestRelease(timeout)
	if err != nil {
		return "", err
	}
	writeUpdateCache(latest)
	return latest, nil
}

// checkForUpdate prints one line when a newer release is published. It runs at
// the end of a bare `niq` — after the report, so the delay (once a day, at most
// a second and a half) never sits between the user and the answer they asked
// for.
func checkForUpdate() {
	if os.Getenv("NIQ_NO_UPDATE_CHECK") != "" {
		return
	}
	// Nothing to compare a dev build against, and nobody to read a hint in a
	// pipe.
	if _, ok := semverParts(version); !ok {
		return
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	latest, err := cachedLatest(passiveCheckTimeout)
	if err != nil || !newerRelease(latest, version) {
		return
	}
	// Every start mentions it: the check is what is rationed (once a day), not
	// the reminder — an update that is still pending is worth seeing until it
	// is taken.
	fmt.Printf("\n%s%s\n",
		brand("niq "+latest), dim(" is available — run ")+bold("niq update"))
}

// runUpdate is `niq update`: check, install with npm, and say what the running
// daemon needs in order to use it.
func runUpdate(args []string) error {
	fs := flag.NewFlagSet("niq update", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Println(bold("niq update") + dim(" — install the latest release with npm"))
	}
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err)
	}
	latest, err := cachedLatest(10 * time.Second)
	if err != nil {
		return fmt.Errorf("could not reach the npm registry: %w", err)
	}
	current := versionNumber()
	// A dev build ("dev", or a version without a release number) is never the
	// latest release, so it is offered the install like an older one.
	if _, ok := semverParts(version); ok && !newerRelease(latest, version) {
		fmt.Printf("%s %s %s\n", bold("niq"), brand(current), dim("is the latest release"))
		return nil
	}

	// A dev build has no release number to show on the left; the install line
	// below carries the version it is moving to either way.
	if _, ok := semverParts(version); ok {
		fmt.Printf("%s %s %s %s\n\n", bold("niq"), brand(current), dim("→"), brand(latest))
	} else {
		fmt.Printf("%s %s\n\n", dim("installing"), brand(latest))
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("npm is not on PATH — install with `npm install -g %s@latest`, or download a binary from https://github.com/niq-run/niq/releases",
			npmPackage)
	}
	cmd := exec.Command(npm, "install", "-g", npmPackage+"@latest")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm install failed: %w", err)
	}

	writeUpdateCache(latest)
	fmt.Printf("\n%s %s\n", bold("niq"), green(fmt.Sprintf("%s installed", latest)))
	// The daemon and the projects are separate processes still running the old
	// binary; -p exists precisely for this.
	fmt.Println(dim("run ") + bold("niq restart -p") + dim(" to put the control plane and the running projects on it"))
	return nil
}
