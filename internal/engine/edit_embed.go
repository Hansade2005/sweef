package engine

import _ "embed"

//go:embed edit_prelude.js.z
var editPreludeJSZ string

func init() {
	registerPreludeZ("edit", editPreludeJSZ)
}
