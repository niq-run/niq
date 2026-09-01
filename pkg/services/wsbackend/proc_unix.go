//go:build unix

package wsbackend

import (
	"os/exec"
	"syscall"
	"time"
)

// setOwnProcessGroup puts the command in its own process group, so killing
// the group reaps every descendant and a backgrounded subprocess cannot
// outlive the command's deadline. Unix only.
func setOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup sends SIGTERM to the whole process group, escalating to SIGKILL
// shortly after so a process that ignores SIGTERM cannot survive.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := -cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	go func() {
		time.Sleep(2 * time.Second)
		_ = syscall.Kill(pgid, syscall.SIGKILL)
	}()
}
