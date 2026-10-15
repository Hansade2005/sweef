package engine

// prelude_embed.go: the prelude ships compiled.
//
// Every engine process used to compile the whole prelude (the DOM, CSSOM and platform the page's scripts see)
// before its first VM, ~180 ms, unless an earlier run had left the bytecode in the cache directory. With an
// empty cache that was the first eval of every fresh CLI session (155 ms against agent-browser's 7 ms). The
// bytecode is now generated at build time (go generate ./internal/engine, part of make embeds; tools/genprelude)
// and embedded deflated with the key of the sources it came from. compiledPrelude uses it when the key still
// matches the prelude this binary builds; TestEmbeddedPreludeBytecodeIsCurrent fails when it does not.

import (
	_ "embed"
	"encoding/hex"
	"strings"
	"unsafe"

	"modernc.org/quickjs"

	"github.com/sweefbrowser/sweef/internal/zpack"
)

//go:embed prelude.qjsbc.z
var preludeBCZ string

//go:embed prelude.qjsbc.key
var preludeBCKey string

// embeddedPrelude returns the embedded bytecode when it was built from this binary's prelude sources, with
// this QuickJS, on a 64-bit build (the generator runs on one).
func embeddedPrelude() []byte {
	if unsafe.Sizeof(uintptr(0)) != 8 || len(preludeBCZ) == 0 {
		return nil
	}
	k := preludeKey()
	if strings.TrimSpace(preludeBCKey) != preludeEmbedKey(k) {
		return nil
	}
	return zpack.Inflate(preludeBCZ)
}

// preludeEmbedKey names the sources and the QuickJS version the bytecode depends on.
func preludeEmbedKey(k [32]byte) string { return hex.EncodeToString(k[:]) + " " + quickjs.Version() }

// GeneratePreludeBytecode compiles the prelude the way compiledPrelude does and returns the bytecode and the
// key to embed with it (tools/genprelude).
func GeneratePreludeBytecode() (bc []byte, key string, err error) {
	vm, err := quickjs.NewVM()
	if err != nil {
		return nil, "", err
	}
	defer vm.Close()
	restore := stripSource(vm)
	bc, err = vm.Compile(fullPrelude(), quickjs.EvalGlobal)
	restore()
	if err != nil {
		return nil, "", err
	}
	return bc, preludeEmbedKey(preludeKey()), nil
}
