//go:build !linux

// Command vschrome needs Linux (Xvfb, XTEST, /proc). This stub only keeps `go build ./...` working elsewhere.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "vschrome: Linux only")
	os.Exit(2)
}
