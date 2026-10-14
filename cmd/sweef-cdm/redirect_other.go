//go:build !linux && !windows

package main

import "syscall"

func redirectStdoutToStderr() error { return syscall.Dup2(2, 1) }
