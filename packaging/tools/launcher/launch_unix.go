//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

const sweefName = "sweef"

func run(target string, argv []string) error {
	return syscall.Exec(target, argv, os.Environ())
}

func reportError(err error) { fmt.Fprintln(os.Stderr, "Sweef launcher:", err) }
