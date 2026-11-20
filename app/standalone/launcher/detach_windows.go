//go:build windows

package launcher

import "syscall"

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

// projectProcAttr starts the project without inheriting the launching console,
// so closing that console does not take it down.
func projectProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}
