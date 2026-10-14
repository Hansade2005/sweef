package engine

// xmldom.go: XML documents for DOMParser and XMLHttpRequest.responseXML (see xml_prelude.js).

import _ "embed"

//go:embed xml_prelude.js
var xmlPreludeJS string

func init() {
	registerPrelude("xmldom", xmlPreludeJS)
}
