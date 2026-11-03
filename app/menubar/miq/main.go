//go:build darwin

// miq starts the macOS menu bar app (`niq` with an m for menu bar).
//
// The app is a .app bundle (built by `make app`); this command only locates it
// and hands it to LaunchServices, so running `niqm` twice focuses the instance
// already in the menu bar instead of starting a second one.
//
// Everything the menu shows and does comes from the niq CLI — `niq status
// --json` for the report, `niq project start|stop|restart <id>` for the
// actions — so this is an entry point, not a second implementation.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	app, err := findApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "miq:", err)
		os.Exit(1)
	}
	args := []string{"-a", app}
	// Hand the app the CLI that sits beside this launcher: the two were built
	// together, and the app launched by LaunchServices sees neither the shell's
	// PATH nor its environment.
	if niq, err := sibling("niq"); err == nil {
		args = append(args, "--args", "--niq", niq)
	}
	if err := exec.Command("open", args...).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "miq: open %s: %v\n", app, err)
		os.Exit(1)
	}
}

// sibling returns the path of another binary installed next to this one.
func sibling(name string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	path := filepath.Join(filepath.Dir(exe), name)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", fmt.Errorf("%s not found next to miq", name)
	}
	return path, nil
}

// findApp looks where the bundle can reasonably be: NIQ_APP, ~/Applications,
// /Applications, and the checkout this binary was built in (bin/miq →
// ../../dist/niq.app).
func findApp() (string, error) {
	var candidates []string
	if v := os.Getenv("NIQ_APP"); v != "" {
		candidates = append(candidates, v)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "Applications", "niq.app"))
	}
	candidates = append(candidates, "/Applications/niq.app")

	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "..", "dist", "niq.app"),
			filepath.Join(dir, "..", "..", "dist", "niq.app"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "dist", "niq.app"))
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(filepath.Join(candidate, "Contents", "Info.plist")); err == nil && !info.IsDir() {
			return filepath.Clean(candidate), nil
		}
	}
	return "", fmt.Errorf("niq.app not found — build it with `make app`, or point NIQ_APP at it")
}
