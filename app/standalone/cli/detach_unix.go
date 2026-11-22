//go:build unix

package cli

import (
	"os"
	"syscall"
)

// detachedProcAttr puts the background control plane in its own session, so
// the shell that launched it is no longer its controlling terminal: closing
// the terminal (SIGHUP to the foreground process group) leaves it running.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminateProcess asks pid to shut down. The control plane treats SIGTERM
// like Ctrl+C: stop accepting, drain, recreate its record file.
func terminateProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
