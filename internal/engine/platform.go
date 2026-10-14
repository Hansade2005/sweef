package engine

// platform.go: the web-platform layer that sits on top of the DOM prelude
// (docs/web-platform.md lists every API and its status).
//
// Start-up cost matters (the prelude is loaded into every VM), so this file
// registers only a small bootstrap, platformjs/boot.js. The bootstrap installs
// lazy accessors for every API in this layer; the implementation of a module
// (platformjs/<name>.js) is fetched through __sw.platformSrc and evaluated the
// first time one of its names is touched. A page that never uses XPath,
// URLPattern, the Navigation API, OPFS... pays one accessor per name and no
// parsing.

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed platformjs/*.js
var platformFS embed.FS

func platformModule(name string) string {
	if strings.ContainsAny(name, "/.\\") {
		return ""
	}
	b, err := platformFS.ReadFile("platformjs/" + name + ".js")
	if err != nil {
		return ""
	}
	return string(b)
}

func init() {
	b, err := platformFS.ReadFile("platformjs/boot.js")
	if err != nil {
		panic(fmt.Sprintf("platform: %v", err))
	}
	registerPrelude("platform", string(b))
	registerHostExtension("platform", func(j *jsVM) map[string]hostFn {
		f := map[string]hostFn{
			"platformSrc": func(a []any) (any, error) { return platformModule(argS(a, 0)), nil },
		}
		j.codecHost(f)
		j.opfsHost(f)
		j.wcHost(f)
		return f
	})
}
