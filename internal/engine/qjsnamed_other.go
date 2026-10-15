//go:build 386 || arm

package engine

import (
	"encoding/json"
	"errors"
	"strings"

	"modernc.org/quickjs"
)

// compileNamed is unavailable where QuickJS values are NaN-boxed; callers compile unnamed (see qjsnamed.go).
func compileNamed(vm *quickjs.VM, src, name string, flags int) ([]byte, error) {
	return nil, errors.New("named compile unavailable")
}

// withImportMetaURL: see qjsnamed.go.
func withImportMetaURL(src, url string) string {
	if strings.HasPrefix(src, "#!") {
		return src
	}
	q, _ := json.Marshal(url) // a JS string literal (strconv.Quote may write \U escapes)
	return "import.meta.url=" + string(q) + ";" + src
}
