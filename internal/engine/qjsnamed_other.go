//go:build 386 || arm

package engine

import (
	"encoding/json"
	"strings"

	"modernc.org/quickjs"
)

// runModuleNamed is unavailable where QuickJS values are NaN-boxed; callers run modules unnamed (qjsnamed.go).
func runModuleNamed(vm *quickjs.VM, src, name string) (bc []byte, err error, handled bool) {
	return nil, nil, false
}

// compileModuleNamed is unavailable here too; the precompiler then leaves modules alone.
func compileModuleNamed(vm *quickjs.VM, src, name string) ([]byte, bool) { return nil, false }

// withImportMetaURL: see qjsnamed.go.
func withImportMetaURL(src, url string) string {
	if strings.HasPrefix(src, "#!") {
		return src
	}
	q, _ := json.Marshal(url) // a JS string literal (strconv.Quote may write \U escapes)
	return "import.meta.url=" + string(q) + ";" + src
}

// pendingDynamicImports is unavailable here (see qjsnamed.go): queued import() calls load one at a time.
func pendingDynamicImports(vm *quickjs.VM) [][2]string { return nil }

// preludeStripsSource: the prelude keeps its source here; nativeToStringJS makes it print as native code.
func preludeStripsSource() bool { return false }

func stripSource(vm *quickjs.VM) (restore func()) { return func() {} }
