//go:build windows

package main

import (
	"os"
	"syscall"
)

// Windows console-process creation flags. syscall does not name
// DETACHED_PROCESS, so the values are spelled out here.
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

// detachedProcAttr starts the background control plane without inheriting the
// launching console, so closing that console does not take it down.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}

// terminateProcess stops pid. Windows has no SIGTERM to deliver, so this is a
// hard stop; the control plane holds no state that a graceful shutdown flushes
// (projects run as their own processes and keep running).
func terminateProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// processAlive reports whether pid still exists. FindProcess opens the process
// on Windows and fails once it is gone.
func processAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
