package engine

import (
	"strings"
	"testing"
	"time"
)

func TestFramesCannotShowBrowserPagesOrLocalFiles(t *testing.T) {
	parent := newSite(t, map[string]string{"/": `<!doctype html>
<iframe src="sweef://settings"></iframe>
<iframe src="file:///etc/passwd"></iframe>`})
	e := newEng(t)
	tab, err := e.NewTab(parent.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range tab.Frames() {
		waitFor(t, "frame load attempt", 3*time.Second, func() bool {
			for _, ev := range f.Tab.ConsoleLog(0) {
				if m, ok := ev.Data.(map[string]string); ok && strings.Contains(m["text"], "Not allowed to load") {
					return true
				}
			}
			return false
		})
		if u := f.Tab.URL(); u != "about:blank" {
			t.Fatalf("frame %d shows %q", i, u)
		}
	}
}

func TestWindowOpenRefusesBrowserPagesAndLocalFiles(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<!doctype html>x`})
	e := newEng(t)
	tab, _ := e.NewTab(s.URL + "/")
	for _, u := range []string{"sweef://settings", "file:///etc/passwd", "view-source:http://example.com/"} {
		tab.NoteUserActivation()
		if got := jsonOf(t, tab, `window.open('`+u+`') === null`); got != "true" {
			t.Fatalf("window.open(%s) opened a window", u)
		}
	}
	if len(e.ListTabs()) != 1 {
		t.Fatalf("tabs: %d", len(e.ListTabs()))
	}
	if n := len(tab.BlockedPopups()); n != 3 {
		t.Fatalf("blocked popups: %d", n)
	}
}

func TestFrameReadFilterHidesFramesFromSnapshotAndRefs(t *testing.T) {
	secret := newSite(t, map[string]string{"/": `<!doctype html><button id=b>Secret</button><script>window.clicks=0;document.getElementById('b').addEventListener('click',function(){window.clicks++})</script>`})
	open := newSite(t, map[string]string{"/": `<!doctype html><button id=b>Open</button>`})
	top := newSite(t, map[string]string{"/": `<!doctype html><h1>Top</h1><iframe src="` + secret.URL + `/"></iframe><iframe src="` + open.URL + `/"></iframe>`})
	e := newEng(t)
	e.SetFrameReadFilter(func(u string) bool { return !strings.HasPrefix(u, secret.URL) })
	tab, err := e.NewTab(top.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	y, err := tab.AriaSnapshotV2(SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(y, "Secret") {
		t.Fatalf("snapshot leaked a frame the filter hides:\n%s", y)
	}
	if !strings.Contains(y, `button "Open"`) {
		t.Fatalf("snapshot lacks the allowed frame:\n%s", y)
	}
	// guessing the ref of the hidden frame does nothing
	c := tab.frameList()[0].child // Frames() hides it, as it should
	if err := tab.Click("f1e1"); err == nil {
		t.Fatal("a ref into a hidden frame was accepted")
	}
	if got := waitEval(t, c, `typeof window.clicks === 'undefined' ? '' : String(window.clicks)`); got != "0" {
		t.Fatalf("hidden frame was clicked: %s", got)
	}
}
