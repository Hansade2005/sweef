//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func groupAttr(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killGroup(c *exec.Cmd) { syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
