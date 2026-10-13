package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A keystroke in the omnibox paints the chrome once: the suggestions are answered inside the same event turn
// (etab.omniSync) instead of arriving as a second update after the first frame.
func TestOmniboxKeystrokeIsOneFrame(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	h.app.prof.History.Visit("https://swan.example/", "Swan Lake", true)
	h.key("ctrl+l")
	h.typeText("sw")
	h.idle()
	for _, r := range "an" {
		p0 := h.app.stats.chromePaints.Load()
		h.typeText(string(r))
		h.idle()
		if n := h.app.stats.chromePaints.Load() - p0; n != 1 {
			t.Errorf("typing %q painted the chrome %d times, want 1", string(r), n)
		}
	}
	var rows []string
	h.chromeEval(`SW.$$("#od .orow .ot").map(function(e){return e.textContent})`, &rows)
	if joined := strings.Join(rows, "|"); !strings.Contains(joined, "Swan Lake") || !strings.Contains(joined, "swan") {
		t.Errorf("the rows of the single frame lack the typed text or our suggestion: %v", rows)
	}
}

// When the UI loop cannot answer within omniSyncWait the page is told so ({stale:true}) and shows what it has;
// the real answer arrives later as an ordinary message and is applied then. The worker is never held for long.
func TestOmniSyncStaleFallback(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	h.app.prof.History.Visit("https://swan.example/", "Swan Lake", true)
	h.key("ctrl+l")
	h.typeText("sw")
	h.idle()
	c := h.app.chromeTab()
	raw, _ := json.Marshal(map[string]any{"text": "swa", "id": 9001, "scope": "bar", "limit": 8})
	// type the text without the page posting (so the request below is the only one), then stall the UI loop
	h.chromeEval(`SW.omniLast.input.value="swa"; SW.omniLast.typed="swa"; SW.omniSeq=9000; SW.omniLast.req[9001]=1; SW.omniLast.wait=9001; SW.omniLast.remote=[]; 1`, nil)
	const hold = 600 * time.Millisecond
	release := make(chan struct{})
	h.app.post(func() { <-release })
	time.AfterFunc(hold, func() { close(release) })
	var took time.Duration
	if err := c.call(func() error {
		t0 := time.Now()
		c.omniSync(raw)
		took = time.Since(t0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if took > hold/2 { // the limit is omniSyncWait; the margin is for a loaded machine
		t.Errorf("the worker waited %v for a UI loop that stays busy for %v (limit %v)", took, hold, omniSyncWait)
	}
	var shown string
	h.chromeEval(`SW.$("#od").textContent`, &shown)
	if !strings.Contains(shown, "swa") || strings.Contains(shown, "Swan Lake") {
		t.Errorf("stale reply should render the typed text only, got %q", shown)
	}
	h.waitFor("late answer applied", func() bool {
		h.chromeEval(`SW.$("#od").textContent`, &shown)
		return strings.Contains(shown, "Swan Lake")
	})
}
