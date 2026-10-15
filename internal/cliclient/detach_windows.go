//go:build windows

package cliclient

import (
	"os/exec"
	"syscall"
)

// detach starts the daemon detached from the console (DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP).
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200, HideWindow: true}
}
