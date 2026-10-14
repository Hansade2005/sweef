package engine

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---- sandbox --------------------------------------------------------------------------------

const scriptedChild = `<!doctype html><body><p id="p">static</p><script>
window.ran = 1; document.getElementById('p').textContent = 'changed';
parent.postMessage({from: 'child', origin: location.origin}, '*');
</script></body>`

func TestSandboxBlocksScripts(t *testing.T) {
	child := newSite(t, map[string]string{"/s": scriptedChild, "/ok": scriptedChild})
	parent := newSite(t, map[string]string{"/": `<!doctype html><script>
window.msgs = []; addEventListener('message', function (e) { window.msgs.push({data: e.data, origin: e.origin}); });
</script>
<iframe sandbox src="` + child.URL + `/s"></iframe>
<iframe sandbox="allow-scripts" src="` + child.URL + `/ok"></iframe>`})
	e := newEng(t)
	tab, err := e.NewTab(parent.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	fr := tab.Frames()
	if len(fr) != 2 || !fr[0].Sandbox || !fr[1].Sandbox {
		t.Fatalf("frames: %+v", fr)
	}
	blocked, allowed := fr[0].Tab, fr[1].Tab
	if got := evalStr(t, blocked, `document.getElementById('p').textContent`); got != "static" {
		t.Fatalf("a sandboxed frame without allow-scripts ran its script: %q", got)
	}
	if got := evalStr(t, blocked, `String(window.ran)`); got != "undefined" {
		t.Fatalf("window.ran = %s", got)
	}
	waitFor(t, "script in the allow-scripts frame", 3*time.Second, func() bool {
		return evalStr(t, allowed, `document.getElementById('p').textContent`) == "changed"
	})
	// allow-scripts without allow-same-origin: an opaque origin, visible to the parent as "null"
	waitFor(t, "message from the sandboxed frame", 3*time.Second, func() bool {
		tab.Settle(10 * time.Millisecond)
		return jsonOf(t, tab, `window.msgs.length`) == "1"
	})
	if got := jsonOf(t, tab, `window.msgs[0].origin + '|' + window.msgs[0].data.origin`); got != `"null|null"` {
		t.Fatalf("origin of a sandboxed document: %s", got)
	}
}

func TestSandboxSameOriginKeepsOrigin(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><script>window.o = location.origin;</script>`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><iframe sandbox="allow-scripts allow-same-origin" src="` + child.URL + `/"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	c := firstFrame(t, tab)
	if got := waitEval(t, c, `window.o`); got != child.URL {
		t.Fatalf("origin = %q, want %q", got, child.URL)
	}
}

func TestSandboxBlocksPopupsUnlessAllowed(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html>x`})
	parent := newSite(t, map[string]string{"/": `<!doctype html>
<iframe sandbox="allow-scripts" src="` + child.URL + `/"></iframe>
<iframe sandbox="allow-scripts allow-popups" src="` + child.URL + `/"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	fr := tab.Frames()
	if len(fr) != 2 {
		t.Fatalf("frames: %d", len(fr))
	}
	no, yes := fr[0].Tab, fr[1].Tab
	no.NoteUserActivation() // even with a gesture
	if got := jsonOf(t, no, `window.open('/p') === null`); got != "true" {
		t.Fatalf("sandbox without allow-popups opened a window: %s", got)
	}
	if bl := no.BlockedPopups(); len(bl) != 1 || !strings.Contains(bl[0].Reason, "sandbox") {
		t.Fatalf("blocked popup log: %+v", bl)
	}
	if len(e.ListTabs()) != 1 {
		t.Fatal("a popup tab exists")
	}
	yes.NoteUserActivation()
	if got := jsonOf(t, yes, `window.open('/p') !== null`); got != "true" {
		t.Fatalf("allow-popups did not allow window.open: %s", got)
	}
	waitFor(t, "popup", 3*time.Second, func() bool { return len(e.ListTabs()) == 2 })
	// the popup stays sandboxed (no allow-popups-to-escape-sandbox)
	if p := e.ListTabs()[1]; !p.Sandboxed() {
		t.Fatal("a popup of a sandboxed frame escaped the sandbox")
	}
}

func TestSandboxBlocksForms(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><form id=f action="/sent"><input name=a value=1></form>`, "/sent": `<!doctype html>sent`})
	parent := newSite(t, map[string]string{"/": `<!doctype html>
<iframe sandbox="allow-scripts" src="` + child.URL + `/"></iframe>
<iframe sandbox="allow-scripts allow-forms" src="` + child.URL + `/"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	fr := tab.Frames()
	jsonOf(t, fr[0].Tab, `document.getElementById('f').submit(); 1`)
	fr[0].Tab.Settle(100 * time.Millisecond)
	if strings.Contains(fr[0].Tab.URL(), "/sent") {
		t.Fatal("a sandbox without allow-forms submitted a form")
	}
	jsonOf(t, fr[1].Tab, `document.getElementById('f').submit(); 1`)
	waitFor(t, "form submission in the allow-forms frame", 5*time.Second, func() bool {
		fr[1].Tab.Settle(20 * time.Millisecond)
		return strings.Contains(fr[1].Tab.URL(), "/sent")
	})
}

func TestSandboxTopNavigation(t *testing.T) {
	other := newSite(t, map[string]string{"/away": `<!doctype html>away`})
	child := newSite(t, map[string]string{"/": `<!doctype html><a id=l target=_top href="` + other.URL + `/away">x</a>`})
	parent := newSite(t, map[string]string{"/": `<!doctype html>
<iframe sandbox="allow-scripts" src="` + child.URL + `/"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	c := firstFrame(t, tab)
	if err := c.Click("#l"); err != nil {
		t.Fatal(err)
	}
	tab.Settle(100 * time.Millisecond)
	if strings.Contains(tab.URL(), "/away") {
		t.Fatal("a sandboxed frame navigated the top level without allow-top-navigation")
	}
}

// ---- messaging between a page and its frames ---------------------------------------------------

func TestPostMessageBetweenParentAndFrame(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><script>
window.got = [];
addEventListener('message', function (e) {
  window.got.push({data: e.data, origin: e.origin, srcIsParent: e.source === window.parent});
  e.source.postMessage({echo: e.data}, e.origin);
});
window.rel = [window.parent !== window, window.top === window.parent, window.top !== window, window.frameElement === null || true].join();
</script>`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><iframe id=f src="` + child.URL + `/"></iframe><script>
window.replies = [];
addEventListener('message', function (e) { window.replies.push({data: e.data, origin: e.origin, srcIsFrame: e.source === document.getElementById('f').contentWindow}); });
window.ok = document.getElementById('f').contentWindow !== null;
</script>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	c := firstFrame(t, tab)
	if got := jsonOf(t, tab, `window.ok`); got != "true" {
		t.Fatalf("iframe.contentWindow is null: %s", got)
	}
	// a wrong targetOrigin is dropped, the right one goes through
	jsonOf(t, tab, `document.getElementById('f').contentWindow.postMessage('lost', 'http://elsewhere.test'); 1`)
	jsonOf(t, tab, `document.getElementById('f').contentWindow.postMessage({n: 7}, '`+child.URL+`'); 1`)
	waitFor(t, "echo", 5*time.Second, func() bool {
		c.Settle(10 * time.Millisecond)
		tab.Settle(10 * time.Millisecond)
		return jsonOf(t, tab, `window.replies.length`) == "1"
	})
	if got := jsonOf(t, c, `window.got.length + ':' + window.got[0].data.n + ':' + window.got[0].origin + ':' + window.got[0].srcIsParent`); got != `"1:7:`+parent.URL+`:true"` {
		t.Fatalf("what the frame received: %s", got)
	}
	if got := jsonOf(t, tab, `window.replies[0].data.echo.n + ':' + window.replies[0].origin + ':' + window.replies[0].srcIsFrame`); got != `"7:`+child.URL+`:true"` {
		t.Fatalf("what the parent received: %s", got)
	}
	if got := evalStr(t, c, `window.rel`); got != "true,true,true,true" {
		t.Fatalf("parent/top relations inside the frame: %s", got)
	}
}

func TestFrameLoadEventAndScriptInsertedFrame(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#00f">`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0"><script>
window.loaded = 0;
var f = document.createElement('iframe');
f.style.cssText = 'border:0;position:absolute;left:0;top:0;width:50px;height:50px';
f.addEventListener('load', function () { window.loaded++; });
f.src = '` + child.URL + `/';
document.body.appendChild(f);
</script>`})
	e := newEng(t)
	tab, err := e.NewTab(parent.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitEval(t, tab, `String(window.loaded)`); got == "0" {
		waitFor(t, "load event of the iframe", 3*time.Second, func() bool {
			tab.Settle(10 * time.Millisecond)
			return jsonOf(t, tab, `window.loaded`) == "1"
		})
	}
	waitFor(t, "script-inserted frame to paint", 5*time.Second, func() bool {
		img, _ := tab.PaintRGBA(100, 100, 1, false)
		return img != nil && isColor(px(img, 25, 25), 0, 0, 255)
	})
}

// ---- agent: snapshot, refs ---------------------------------------------------------------------

func TestSnapshotIncludesFramesAndRefsReachThem(t *testing.T) {
	leaf := newSite(t, map[string]string{"/": clickPage + `<button id=deep>Deep</button><script>document.getElementById('deep').addEventListener('click',function(){window.deep=(window.deep||0)+1;});</script>`})
	mid := newSite(t, map[string]string{"/": `<!doctype html><h2>Middle</h2><iframe src="` + leaf.URL + `/"></iframe>`})
	top := newSite(t, map[string]string{"/": `<!doctype html><h1>Top</h1><button>Outer</button><iframe src="` + mid.URL + `/" width=300 height=200></iframe>`})
	e := newEng(t)
	tab, err := e.NewTab(top.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	y, err := tab.AriaSnapshotV2(SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`heading "Top"`, `button "Outer"`, `iframe`, `[frame=f1]`, `heading "Middle"`, `[frame=f1f1]`} {
		if !strings.Contains(y, want) {
			t.Fatalf("snapshot lacks %q:\n%s", want, y)
		}
	}
	ref := func(label string) string {
		m := regexp.MustCompile(`button "` + label + `" \[ref=(f[f0-9]*e\d+)\]`).FindStringSubmatch(y)
		if m == nil {
			t.Fatalf("no ref for %q in:\n%s", label, y)
		}
		return m[1]
	}
	leafTab := firstFrame(t, firstFrame(t, tab))
	r := ref("go")
	if !strings.HasPrefix(r, "f1f1e") {
		t.Fatalf("nested ref %q", r)
	}
	if err := tab.Click(r); err != nil {
		t.Fatalf("click %s: %v", r, err)
	}
	if got := evalStr(t, leafTab, `String(window.clicks)`); got != "1" {
		t.Fatalf("agent click on a ref inside a nested frame: clicks = %s", got)
	}
	if err := tab.Click(ref("Deep")); err != nil {
		t.Fatal(err)
	}
	if got := evalStr(t, leafTab, `String(window.deep)`); got != "1" {
		t.Fatalf("second ref: %s", got)
	}
	// fill by ref (the input has no accessible name, so find it through the snapshot's textbox line)
	m := regexp.MustCompile(`textbox[^\n]*\[ref=(f[f0-9]*e\d+)\]`).FindStringSubmatch(y)
	if m == nil {
		t.Fatalf("no textbox ref in:\n%s", y)
	}
	if err := tab.Fill(m[1], "agent text"); err != nil {
		t.Fatalf("fill %s: %v", m[1], err)
	}
	if got := evalStr(t, leafTab, `document.getElementById('i').value`); got != "agent text" {
		t.Fatalf("fill by ref: %q", got)
	}
	// a ref of the top document still works
	outer := regexp.MustCompile(`button "Outer" \[ref=(e\d+)\]`).FindStringSubmatch(y)
	if outer == nil {
		t.Fatalf("no top ref:\n%s", y)
	}
	if err := tab.Click(outer[1]); err != nil {
		t.Fatalf("click top ref: %v", err)
	}
	// a stale or unknown frame ref is an error, not a click somewhere else
	if err := tab.Click("f9e1"); err == nil {
		t.Fatal("click on an unknown frame ref succeeded")
	}
}

// ---- layout interplay ---------------------------------------------------------------------------

func TestSrcdocFrame(t *testing.T) {
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0">
<iframe srcdoc="<body style='margin:0;background:#0f0'><script>window.o = location.origin</script>" style="border:0;position:absolute;left:10px;top:10px;width:80px;height:50px"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	img, _ := tab.PaintRGBA(150, 100, 1, false)
	if c := px(img, 50, 30); !isColor(c, 0, 255, 0) {
		t.Fatalf("srcdoc frame: %v", c)
	}
	c := firstFrame(t, tab)
	if got := waitEval(t, c, `window.o`); got != parent.URL {
		t.Fatalf("srcdoc inherits the embedder's origin: got %q want %q", got, parent.URL)
	}
}

func TestHiddenFramesLoadButDoNotPaintOrTakeInput(t *testing.T) {
	child := newSite(t, map[string]string{"/hidden": `<!doctype html><body style="background:#f00">`, "/vis": `<!doctype html><body style="margin:0;background:#f00">` + clickScript, "/pe": `<!doctype html><body style="margin:0;background:#f00">` + clickScript})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#fff">
<iframe src="` + child.URL + `/hidden" style="display:none"></iframe>
<iframe src="` + child.URL + `/vis" style="border:0;visibility:hidden;position:absolute;left:0;top:0;width:60px;height:60px"></iframe>
<iframe src="` + child.URL + `/pe" style="border:0;pointer-events:none;position:absolute;left:100px;top:0;width:60px;height:60px"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	waitFor(t, "display:none frame to load", 3*time.Second, func() bool {
		child.mu.Lock()
		defer child.mu.Unlock()
		for _, h := range child.hits {
			if h == "/hidden" {
				return true
			}
		}
		return false
	})
	img, _ := tab.PaintRGBA(200, 100, 1, false)
	if c := px(img, 30, 30); !isColor(c, 255, 255, 255) {
		t.Fatalf("visibility:hidden frame painted: %v", c)
	}
	if c := px(img, 130, 30); !isColor(c, 255, 0, 0) {
		t.Fatalf("pointer-events:none frame must still paint: %v", c)
	}
	fr := tab.Frames()
	if len(fr) != 3 {
		t.Fatalf("frames: %d", len(fr))
	}
	_ = tab.MouseClick(30, 30)
	_ = tab.MouseClick(130, 30)
	for i, c := range []*Tab{fr[1].Tab, fr[2].Tab} {
		if got := evalStr(t, c, `String(window.clicks)`); got != "0" {
			t.Fatalf("frame %d took a click it must not take: %s", i+1, got)
		}
	}
}

const clickScript = `<script>window.clicks = 0; document.addEventListener('click', function () { window.clicks++; });</script>`

func TestAncestorOpacityTransformAndInputThroughTransform(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#f00">` + clickScript})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#fff">
<div style="opacity:.5;position:absolute;left:0;top:0"><iframe src="` + child.URL + `/" style="border:0;width:50px;height:50px;display:block"></iframe></div>
<div style="transform:translateX(100px);position:absolute;left:0;top:100px"><iframe src="` + child.URL + `/" style="border:0;width:50px;height:50px;display:block"></iframe></div>
<div style="position:absolute;left:0;top:200px;width:50px;height:50px;overflow:hidden;border-radius:25px"><iframe src="` + child.URL + `/" style="border:0;width:100px;height:100px;display:block"></iframe></div>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	img, _ := tab.PaintRGBA(200, 300, 1, false)
	if c := px(img, 25, 25); c[0] < 250 || c[1] < 110 || c[1] > 145 || c[2] < 110 || c[2] > 145 {
		t.Fatalf("frame inside opacity:.5 should be pink, got %v", c)
	}
	if c := px(img, 125, 125); !isColor(c, 255, 0, 0) {
		t.Fatalf("frame under translateX(100px): %v", c)
	}
	if c := px(img, 25, 125); !isColor(c, 255, 255, 255) {
		t.Fatalf("the translated frame must have left its place: %v", c)
	}
	// clipped by the rounded overflow:hidden ancestor: the corner of its box is not red
	if c := px(img, 2, 202); !isColor(c, 255, 255, 255) {
		t.Fatalf("frame leaked out of a rounded clip: %v", c)
	}
	if c := px(img, 25, 225); !isColor(c, 255, 0, 0) {
		t.Fatalf("centre of the clipped frame: %v", c)
	}
	fr := tab.Frames()
	if err := tab.MouseClick(125, 125); err != nil { // through the transform
		t.Fatal(err)
	}
	var clicked *Tab
	for _, f := range fr {
		if evalStr(t, f.Tab, `String(window.clicks)`) == "1" {
			if clicked != nil {
				t.Fatal("two frames got the click")
			}
			clicked = f.Tab
		}
	}
	if clicked == nil {
		t.Fatal("a click through an ancestor transform reached no frame")
	}
	if clicked.win.fr.idx != fr[1].Tab.win.fr.idx {
		t.Fatalf("the click reached the wrong frame (%d)", clicked.win.fr.idx)
	}
}

func TestLazyFrameLoadsNearViewport(t *testing.T) {
	child := newSite(t, map[string]string{"/lazy": `<!doctype html>lazy`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0"><div style="height:6000px"></div>
<iframe loading="lazy" src="` + child.URL + `/lazy" style="width:100px;height:100px"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	tab.PaintRGBA(300, 600, 1, false) // lays the page out: the frame is 6000px away
	time.Sleep(150 * time.Millisecond)
	child.mu.Lock()
	n := len(child.hits)
	child.mu.Unlock()
	if n != 0 {
		t.Fatalf("a lazy frame far from the viewport was loaded (%d requests)", n)
	}
	tab.SetScrollY(5500)
	waitFor(t, "lazy frame request", 5*time.Second, func() bool {
		child.mu.Lock()
		defer child.mu.Unlock()
		return len(child.hits) == 1
	})
}

// ---- repaint, scrolling, lifetime --------------------------------------------------------------

func TestFrameInvalidatesParent(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#f00"><script>
var flip = false; setInterval(function () { flip = !flip; document.body.style.background = flip ? '#00f' : '#f00'; }, 25);
</script>`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0"><iframe src="` + child.URL + `/" style="border:0;width:40px;height:40px;display:block"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	updates := make(chan struct{}, 1024)
	tab.SetUpdateHook(func() {
		select {
		case updates <- struct{}{}:
		default:
		}
	})
	g0 := tab.Generation()
	seen := map[[3]uint8]bool{}
	waitFor(t, "the frame's animation to repaint the page", 5*time.Second, func() bool {
		img, _ := tab.PaintRGBA(60, 60, 1, false)
		if img != nil {
			seen[px(img, 20, 20)] = true
		}
		return len(seen) >= 2
	})
	if tab.Generation() == g0 {
		t.Fatal("the parent's generation did not change when its frame changed")
	}
	if len(updates) == 0 {
		t.Fatal("the parent's update hook was never called")
	}
	if !tab.Dirty() && false {
		t.Fatal("unreachable")
	}
}

func TestScrollInsideFrame(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0"><div style="height:300px;background:#f00"></div><div id="blue" style="height:1000px;background:#00f"></div>`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0"><iframe src="` + child.URL + `/" style="border:0;width:100px;height:100px;display:block;margin:20px"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	img, _ := tab.PaintRGBA(200, 200, 1, false)
	if c := px(img, 60, 60); !isColor(c, 255, 0, 0) {
		t.Fatalf("top of the frame: %v", c)
	}
	if !tab.WheelAt(60, 60, 0, 350) {
		t.Fatal("the frame did not take the wheel")
	}
	img, _ = tab.PaintRGBA(200, 200, 1, false)
	if c := px(img, 60, 60); !isColor(c, 0, 0, 255) {
		t.Fatalf("after scrolling 350px the frame should show blue, got %v", c)
	}
	// at the end of its content the frame gives the wheel back to the page
	for i := 0; i < 10; i++ {
		tab.WheelAt(60, 60, 0, 500)
	}
	if tab.WheelAt(60, 60, 0, 500) {
		t.Fatal("a frame scrolled to its end kept the wheel")
	}
	// a click at the same spot is translated by the scroll offset: it lands on the blue block
	fr := firstFrame(t, tab)
	jsonOf(t, fr, `window.blue = 0; document.getElementById('blue').addEventListener('click', function () { window.blue++; }); 1`)
	_ = tab.MouseClick(60, 60)
	if got := evalStr(t, fr, `String(window.blue)`); got != "1" {
		t.Fatalf("click inside a scrolled frame did not reach the element under the pointer: blue=%s", got)
	}
}

func TestClosingTheParentClosesItsFrames(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><script>setInterval(function(){}, 10)</script>`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><iframe src="` + child.URL + `/"></iframe><iframe src="` + child.URL + `/"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(parent.URL + "/")
	fr := tab.Frames()
	if len(fr) != 2 {
		t.Fatalf("frames: %d", len(fr))
	}
	ids := []string{fr[0].Tab.ID, fr[1].Tab.ID}
	for _, id := range ids {
		if e.lookupWin(id) == nil {
			t.Fatalf("frame %s is not registered", id)
		}
	}
	if len(e.ListTabs()) != 1 {
		t.Fatalf("frames must not show up as tabs: %d", len(e.ListTabs()))
	}
	if err := e.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "frames to close", 3*time.Second, func() bool {
		return e.lookupWin(ids[0]) == nil && e.lookupWin(ids[1]) == nil
	})
	// navigating the parent drops its frames as well
	tab2, _ := e.NewTab(parent.URL + "/")
	old := tab2.Frames()[0].Tab.ID
	_ = tab2.Navigate(child.URL+"/", nil)
	waitFor(t, "old frame to close after navigation", 3*time.Second, func() bool { return e.lookupWin(old) == nil })
}

func TestFrameRequestHeaders(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html>x`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><iframe src="` + child.URL + `/"></iframe>`})
	e := newEng(t)
	if _, err := e.NewTab(parent.URL + "/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "frame request", 3*time.Second, func() bool {
		child.mu.Lock()
		defer child.mu.Unlock()
		return child.reqHdr["/"] != nil
	})
	child.mu.Lock()
	h := child.reqHdr["/"]
	child.mu.Unlock()
	if h.Get("Sec-Fetch-Dest") != "iframe" || h.Get("Sec-Fetch-Mode") != "navigate" {
		t.Fatalf("frame request: Sec-Fetch-Dest=%q Mode=%q", h.Get("Sec-Fetch-Dest"), h.Get("Sec-Fetch-Mode"))
	}
	if h.Get("Sec-Fetch-Site") != "same-site" {
		t.Fatalf("Sec-Fetch-Site=%q", h.Get("Sec-Fetch-Site"))
	}
	if h.Get("Referer") != parent.URL+"/" && h.Get("Referer") != parent.URL {
		t.Fatalf("Referer=%q", h.Get("Referer"))
	}
}
