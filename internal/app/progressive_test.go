package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/headless"
)

// gatedSite serves a page whose blocking script is held until release is closed.
func gatedSite(t *testing.T) (srv *httptest.Server, release func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Gated</title></head><body style="margin:0">
<div style="height:200px;background:#2f9e44"></div><script src="/gate.js"></script>
<div style="height:200px;background:#2255cc"></div><script>document.title = 'Gated ' + (window.gateOpen ? 'open' : 'closed');</script></body></html>`)
	})
	mux.HandleFunc("/gate.js", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-time.After(20 * time.Second):
		}
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "window.gateOpen = true;")
	})
	srv = httptest.NewServer(mux)
	rel := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() { rel(); srv.Close() })
	return srv, rel
}

// The window shows the page as soon as its HTML is in: a blocking script that is still downloading neither holds
// the first frame nor the input, and the page finishes (title set by the late inline script) when the script arrives.
func TestPageIsShownBeforeABlockingScriptArrives(t *testing.T) {
	srv, release := gatedSite(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{"about:blank"}}})
	h.waitFor("initial tab loaded", func() bool { return h.tab().URL == "about:blank" && !h.tab().Loading })
	h.idle()
	h.key("ctrl+l")
	h.typeText(srv.URL + "/")
	start := time.Now()
	for _, e := range headless.KeyEvents("Enter") { // not h.key: that waits for the app to go idle, which a held load never does
		h.win.Push(e)
	}
	h.waitFor("first frame while the script is held", func() bool {
		ok := near(h.px(40, vpTop+50), "#2f9e44")
		if !ok && time.Since(start) > 5*time.Second {
			t.Logf("still waiting: url=%q loading=%v title=%q px=%v pending=%d", h.tab().URL, h.tab().Loading, h.tab().Title, h.px(40, vpTop+50), h.app.pending.Load())
			time.Sleep(time.Second)
		}
		return ok
	})
	if !h.tab().Loading {
		t.Fatal("the tab must still show it is loading while the script is outstanding")
	}
	if h.tab().Title == "Gated closed" || h.tab().Title == "Gated open" {
		t.Fatalf("the late inline script ran before the held script arrived: %q", h.tab().Title)
	}
	// the tab's worker is not parked on the held script: an op queued now runs at once (input, scroll and paint
	// requests reach the page while it loads)
	t0 := time.Now()
	var title string
	h.pageEval(`document.title`, &title)
	if d := time.Since(t0); d > 400*time.Millisecond {
		t.Fatalf("an operation on the loading tab waited %v behind the held script", d)
	}
	release()
	h.waitTitle("Gated open")
	h.waitFor("load finished", func() bool { return !h.tab().Loading })
	h.wantPx(40, vpTop+250, "#2255cc")
}

// The agent drives tabs like the CLI does: it gets the settled page, never the first frame.
func TestAgentNavigateWaitsForTheSettledPage(t *testing.T) {
	srv, release := gatedSite(t)
	h := newHarness(t, hopts{cfg: Config{}})
	h.idle()
	id := h.app.model.Active(h.app.winID)
	go func() { time.Sleep(600 * time.Millisecond); release() }()
	t0 := time.Now()
	if err := h.app.AgentNavigate(context.Background(), id, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < 500*time.Millisecond {
		t.Fatalf("AgentNavigate returned after %v, before the page's script arrived", d)
	}
	var got string
	h.pageEval(`document.title + '|' + window.gateOpen`, &got)
	if got != "Gated open|true" {
		t.Fatalf("page state after AgentNavigate: %q", got)
	}
}

// about:blank is an empty document, not the offline page.
func TestAboutBlankInTheOmnibox(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	h.key("ctrl+l")
	h.typeText("about:blank")
	h.key("Enter")
	h.waitFor("about:blank", func() bool { return h.tab().URL == "about:blank" && !h.tab().Loading })
	var got string
	h.pageEval(`location.href + '|' + document.body.innerText.trim() + '|' + document.documentElement.outerHTML.length`, &got)
	if len(got) < 12 || got[:12] != "about:blank|" || got[12] != '|' {
		t.Fatalf("about:blank shows content: %q", got)
	}
	if h.app.activePage().loadErr.Load() != nil {
		if s, _ := h.app.activePage().loadErr.Load().(string); s != "" {
			t.Fatalf("load error %q", s)
		}
	}
}
