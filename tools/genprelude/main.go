// Command genprelude writes the engine's compiled prelude (internal/engine/prelude.qjsbc.z, deflated) and the
// key of the sources it was compiled from (prelude.qjsbc.key). Run by go generate ./internal/engine after the
// preludes' .js.z files are rebuilt; see internal/engine/prelude_embed.go.
package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/zpack"
)

func main() {
	bc, key, err := engine.GeneratePreludeBytecode()
	if err != nil {
		fmt.Fprintln(os.Stderr, "genprelude:", err)
		os.Exit(1)
	}
	if old, err := os.ReadFile("prelude.qjsbc.key"); err == nil && string(bytes.TrimSpace(old)) == key {
		if z, err := os.ReadFile("prelude.qjsbc.z"); err == nil && bytes.Equal(zpack.Inflate(string(z)), bc) {
			return // unchanged: keep the files (and their timestamps)
		}
	}
	if err := os.WriteFile("prelude.qjsbc.z", zpack.Deflate(bc), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genprelude:", err)
		os.Exit(1)
	}
	if err := os.WriteFile("prelude.qjsbc.key", []byte(key+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genprelude:", err)
		os.Exit(1)
	}
	fmt.Printf("genprelude: %d bytes of bytecode, %d deflated\n", len(bc), len(zpack.Deflate(bc)))
}
