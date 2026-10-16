package engine

import "testing"

func TestInnerTextFollowsRendering(t *testing.T) {
	e := New(Options{CacheDir: "off"})
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetDocument(`<!doctype html><body><div id="r"><h1><span style="display:block">The Future of</span><span style="display:block">Workplace   Readiness</span></h1>
<p>One <b>two</b>
three</p><p style="display:none">hidden</p><span style="visibility:hidden">invisible</span>a<br>b<pre>x  y</pre>
<table><tr><td>c1</td><td>c2</td></tr><tr><td>d1</td><td>d2</td></tr></table></div></body>`, "https://it.test/")
	got := mustEval(t, tab, `document.getElementById('r').innerText`)
	want := `"The Future of\nWorkplace Readiness\n\nOne two three\n\na\nb\nx  y\nc1\tc2\nd1\td2"`
	if got != want {
		t.Fatalf("innerText:\n got %s\nwant %s", got, want)
	}
}
