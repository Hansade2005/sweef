package engine

// xmldom.go: XML documents for DOMParser and XMLHttpRequest.responseXML (see xml_prelude.js).

import _ "embed"

//go:embed xml_prelude.js.z
var xmlPreludeJSZ string

func init() {
	registerPreludeZ("xmldom", xmlPreludeJSZ)
}
