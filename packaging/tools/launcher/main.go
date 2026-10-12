// Command launcher is the tiny "Sweef" entry point used as the macOS bundle
// executable and the Windows Start-menu/default-browser target. It runs the
// sibling `sweef` binary as `sweef gui <args...>`, so URLs and files passed by
// the OS reach the browser. Pure Go, no CGO.
//
// On Windows it is built with -H=windowsgui so that no console window flashes,
// and it starts sweef detached without a console. Elsewhere it exec()s.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	self, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	target := filepath.Join(filepath.Dir(self), sweefName)
	if _, err := os.Stat(target); err != nil {
		fatal(fmt.Errorf("cannot find %s next to the launcher: %w", sweefName, err))
	}
	argv := append([]string{target, "gui"}, os.Args[1:]...)
	fatal(run(target, argv))
}

func fatal(err error) {
	if err != nil {
		reportError(err)
		os.Exit(1)
	}
}
