//go:build windows

package wsbackend

import (
	"os/exec"
)

// setOwnProcessGroup is a no-op on Windows: process groups are a Unix
// concept (JOB objects would be the native equivalent).
func setOwnProcessGroup(cmd *exec.Cmd) {}

// killGroup terminates the direct child process. Windows has no process
// groups here, so descendants are not reaped.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
