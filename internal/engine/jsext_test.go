package engine

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/jsext"
)

func init() {
	jsext.Register(jsext.Extension{
		Name:    "ztest",
		Order:   1000,
		Prelude: `var I = __swInternals; var sw = I.sw; I.global.__extTag = function (el) { return sw.ztest_tag(I.idOf(el)); };`,
		Funcs: map[string]jsext.Func{
			"tag": func(h jsext.Host, a []any) (any, error) {
				n := h.Node(jsext.Int(a, 0))
				if n == nil {
					return "", nil
				}
				return n.Data, nil
			},
		},
	})
}

func TestJSExtensionHooks(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/inline")
	if got := mustEval(t, tab, `__extTag(document.querySelector('h1'))`); got != `"h1"` {
		t.Fatalf("extension call = %s", got)
	}
	if got := mustEval(t, tab, `typeof __swInternals`); got != `"undefined"` {
		t.Fatalf("internals leaked: %s", got)
	}
}
