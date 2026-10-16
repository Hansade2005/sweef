//go:build !windows

package cliclient

import (
	"os/exec"
	"syscall"
)

// detach puts the daemon in its own session so it outlives the CLI call.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
