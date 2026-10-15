package engine

import (
	"testing"
	"time"
)

// Element.animate runs: computed values interpolate while it plays (inline style untouched), fill:forwards keeps
// the end value, fill none drops the effect, finish fires and finished resolves.
func TestWebAnimations(t *testing.T) {
	e := New(Options{CacheDir: "off"})
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetDocument(`<!doctype html><body><div id="a" style="opacity:0"></div><div id="b" style="opacity:0.3"></div><script>
window.ev = [];
var a = document.getElementById('a').animate([{opacity: 0, transform: 'translateX(0px)'}, {opacity: 1, transform: 'translateX(100px)'}], {duration: 2500, fill: 'forwards'});
a.onfinish = function () { ev.push('a-finish'); };
a.finished.then(function () { ev.push('a-finished'); });
var b = document.getElementById('b').animate({opacity: [1, 0.5]}, 200);
b.addEventListener('finish', function () { ev.push('b-finish'); });
</script></body>`, "https://waapi.test/")
	mid := mustEval(t, tab, `(function(){ var o = +getComputedStyle(document.getElementById('a')).opacity; return o > 0 && o < 1; })()`)
	if mid != "true" {
		t.Fatalf("mid-animation opacity: %s", mid)
	}
	tab.RunFor(3000 * time.Millisecond)
	got := mustEval(t, tab, `[getComputedStyle(document.getElementById('a')).opacity, document.getElementById('a').style.opacity, getComputedStyle(document.getElementById('b')).opacity, ev.sort().join(','), document.getElementById('a').getAnimations().length].join('|')`)
	if got != `"1|0|0.3|a-finish,a-finished,b-finish|0"` {
		t.Fatalf("after: %s", got)
	}
}
