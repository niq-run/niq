//go:build unix

package launcher

import "syscall"

// projectProcAttr puts the project in its own session, so the shell that
// launched it is not its controlling terminal: closing the terminal leaves the
// project running, and a shell restart never takes projects down with it.
func projectProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
