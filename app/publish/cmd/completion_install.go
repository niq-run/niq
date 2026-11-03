// `niq completion install` writes the completion script into a directory the
// shell already scans, so nobody has to edit a dotfile — the same thing a
// package manager does when it ships a completion file.
//
//	zsh    every directory in $fpath is read at startup; the first writable one
//	       wins (Homebrew's site-functions, or oh-my-zsh's custom/completions)
//	fish   ~/.config/fish/completions is loaded automatically
//	bash   bash-completion's user drop-in directory
//
// It is idempotent: re-running after an upgrade replaces the file.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runCompletionInstall(args []string) error {
	fs := flag.NewFlagSet("niq completion install", flag.ContinueOnError)
	shell := fs.String("shell", "", "Shell to install for (default: $SHELL)")
	dry := fs.Bool("dry-run", false, "Print the target path instead of writing")
	fs.Usage = func() {
		fmt.Println(bold("niq completion install") + dim(" — write the script where your shell already looks"))
	}
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err)
	}

	name := *shell
	if name == "" {
		name = filepath.Base(os.Getenv("SHELL"))
	}
	switch name {
	case "zsh", "bash", "fish":
	default:
		return fmt.Errorf("cannot tell which shell to install for (SHELL=%q); pass --shell zsh|bash|fish", os.Getenv("SHELL"))
	}

	dir, file, note, err := completionTarget(name)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, file)
	if *dry {
		fmt.Println(path)
		return nil
	}

	script := map[string]string{"zsh": zshCompletion, "bash": bashCompletion, "fish": fishCompletion}[name]
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(script), 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("%s %s\n", bold("completion"), dim("→ "+shortPath(path)))
	if note != "" {
		fmt.Println(dim(note))
	}
	fmt.Println(dim("open a new shell to pick it up"))
	return nil
}

// completionTarget answers where this shell looks for completion files. note is
// an optional caveat to print when the answer depends on something else.
func completionTarget(shell string) (dir, file, note string, err error) {
	switch shell {
	case "zsh":
		dir, err = zshCompletionDir()
		return dir, "_niq", "", err
	case "fish":
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, herr := os.UserHomeDir()
			if herr != nil {
				return "", "", "", herr
			}
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "fish", "completions"), "niq.fish", "", nil
	default: // bash
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", "", "", herr
		}
		if !bashCompletionInstalled() {
			note = "bash-completion is not installed — install it (brew install bash-completion@2) and source it from ~/.bashrc"
		}
		return filepath.Join(home, ".local", "share", "bash-completion", "completions"), "niq", note, nil
	}
}

// zshCompletionDir asks zsh itself for its fpath and picks a directory it can
// write to, preferring the conventional shared one over an internal directory
// of a framework.
func zshCompletionDir() (string, error) {
	out, err := exec.Command("zsh", "-c", "print -l $fpath").Output()
	if err != nil {
		return "", fmt.Errorf("asking zsh for its fpath: %w", err)
	}
	home, _ := os.UserHomeDir()
	prefix := home + string(filepath.Separator)

	// A directory under the user's home can simply be created; anything else
	// has to exist and accept writes already.
	var dirs []string
	seen := map[string]bool{}
	for _, dir := range strings.Split(string(out), "\n") {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		switch {
		case home != "" && strings.HasPrefix(dir, prefix):
			dirs = append(dirs, dir)
		case dirWritable(dir):
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		return "", errors.New("no usable directory in zsh's fpath; run `niq completion zsh` and place it there yourself")
	}
	best, bestScore := dirs[0], 1<<30
	for _, dir := range dirs {
		if score := zshDirScore(dir); score < bestScore {
			best, bestScore = dir, score
		}
	}
	return best, nil
}

// zshDirScore ranks a writable fpath entry, lowest first. A directory of the
// user's own beats a package manager's: it survives Homebrew and framework
// upgrades, and nothing else manages it. Inside oh-my-zsh, custom/completions is
// the designed place for this — the directories beside it belong to the
// framework and are replaced when it updates.
func zshDirScore(dir string) int {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(dir, home+string(filepath.Separator)) {
		switch {
		case strings.Contains(dir, filepath.Join("custom", "completions")):
			return 0
		case !strings.Contains(dir, string(filepath.Separator)+".oh-my-zsh"+string(filepath.Separator)):
			return 1
		default:
			return 4
		}
	}
	if strings.Contains(dir, "site-functions") {
		return 2
	}
	return 3
}

// dirWritable reports whether a directory exists and accepts files, without
// leaving anything behind.
func dirWritable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".niq-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// bashCompletionInstalled looks for the real thing on the usual paths — without
// it, the drop-in file is read by nobody and the install would look successful
// while doing nothing.
func bashCompletionInstalled() bool {
	for _, p := range []string{
		"/opt/homebrew/etc/profile.d/bash_completion.sh",
		"/usr/local/etc/profile.d/bash_completion.sh",
		"/usr/share/bash-completion/bash_completion",
		"/etc/bash_completion",
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
