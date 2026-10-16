package engine

import "testing"

// matchMedia answers like the stylesheets (and like headless Chromium on a desktop): range syntax, hover and
// pointer of a mouse, no reduced motion; getComputedStyle reports the cascade's keyword properties.
func TestMatchMediaAndComputedKeywords(t *testing.T) {
	e := New(Options{CacheDir: "off"})
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetViewport(390, 844, 1, false, false)
	tab.SetDocument(`<style>.t{text-align:center;flex-direction:column;display:flex;white-space:nowrap}</style><div id="a" class="t">x</div>`, "https://mq.test/")
	got := mustEval(t, tab, `["(width < 48rem)", "(width >= 80rem)", "(hover: hover)", "(pointer: fine)", "(prefers-reduced-motion: reduce)", "(min-width: 300px)"].map(function (q) { return matchMedia(q).matches; }).join(',') + '|' + (function (s) { return [s.textAlign, s.flexDirection, s.whiteSpace].join(','); })(getComputedStyle(document.getElementById('a')))`)
	if got != `"true,false,true,true,false,true|center,column,nowrap"` {
		t.Fatalf("got %s", got)
	}
}
