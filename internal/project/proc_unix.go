//go:build unix

package project

import (
	"os/exec"
	"syscall"
)

// setOwnProcessGroup runs the command in its own process group so cancelling
// the context can kill the whole tree (a launcher like npx or sh spawns
// children that would otherwise leak after the direct child dies). Unix only.
func setOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree force-kills the command's process group.
func killTree(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
