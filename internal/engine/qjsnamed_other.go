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

// withImportMetaURL: see qjsnamed.go.
func withImportMetaURL(src, url string) string {
	if strings.HasPrefix(src, "#!") {
		return src
	}
	q, _ := json.Marshal(url) // a JS string literal (strconv.Quote may write \U escapes)
	return "import.meta.url=" + string(q) + ";" + src
}
