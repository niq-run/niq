//go:build windows

package project

import (
	"os/exec"
)

// setOwnProcessGroup is a no-op on Windows: process groups are a Unix
// concept (JOB objects would be the native equivalent).
func setOwnProcessGroup(cmd *exec.Cmd) {}

// killTree kills the direct child process; descendants are not reaped.
func killTree(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
