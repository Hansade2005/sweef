//go:build linux

package main

import "golang.org/x/sys/unix"

func redirectStdoutToStderr() error { return unix.Dup3(2, 1, 0) }
