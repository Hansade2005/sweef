package app

import (
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
)

// realDSRig runs the app against the real chrome UI with the fake data engine.
func realDSRig(t *testing.T, cfg Config) *dsRig {
	t.Helper()
	srv := pageServer(t)
	f := newFakeData()
	clk := &testClock{t: time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)}
	cfg.DataEngine, cfg.Now = f, clk.now
	if cfg.InitialURLs == nil {
		cfg.InitialURLs = []string{srv.URL + "/two"}
	}
	h := realUI(t, cfg)
	h.waitTitle("Page Two")
	h.waitUIReady()
	return &dsRig{harness: h, f: f, clk: clk, srv: srv}
}

func TestRealUIMeterChipAndPopover(t *testing.T) {
	r := realDSRig(t, Config{})
	mb := int64(store.MB)
	chipHidden := func() (hidden bool) {
		r.chromeEval(`document.getElementById("databtn").classList.contains("hide")`, &hidden)
		return
	}
	if !chipHidden() {
		t.Fatal("with no budget and the saver off, the chip should not take any room")
	}
	r.setBudget(map[string]any{"enabled": true, "limitMB": 100})
	r.f.add(r.defCtx(), 39*mb, map[string]int64{"https://www.example.org": 39 * mb})
	e := r.app.activePage()
	r.f.setTab(e.t, DataSnap{ByKind: map[string]int64{"document": 655_360}, CacheHitBytes: 317_440, Skipped: map[string]int{"image": 12, "font": 11}, SkippedEstimate: 1_992_294})
	r.poll()
	r.waitFor("chip shown", func() bool { return !chipHidden() })
	var txt string
	r.chromeEval(`document.getElementById("databtn").textContent`, &txt)
	if !strings.Contains(txt, "39 MB") || !strings.Contains(txt, "100 MB") {
		t.Fatalf("chip text %q", txt)
	}
	var cls string
	r.chromeEval(`document.getElementById("databtn").className`, &cls)
	if strings.Contains(cls, "w1") || strings.Contains(cls, "w2") || strings.Contains(cls, " x") {
		t.Fatalf("39%% must not look like a warning: %q", cls)
	}
	// 78% turns warm, never red
	r.f.add(r.defCtx(), 39*mb, nil)
	r.poll()
	r.waitFor("warm chip", func() bool {
		r.chromeEval(`document.getElementById("databtn").className`, &cls)
		return strings.Contains(cls, "w1")
	})
	// open the popover: measured and estimated are labelled and separate
	r.chromeEval(`document.getElementById("databtn").click(); 1`, nil)
	r.waitFor("popover", func() bool {
		var n float64
		r.chromeEval(`document.querySelectorAll(".dpop .dpill").length`, &n)
		return n >= 3
	})
	var pills []string
	r.chromeEval(`Array.prototype.map.call(document.querySelectorAll(".dpop .drow"), function(r){ var p = r.querySelector(".dpill"); return (p ? p.textContent : "") + "|" + r.querySelector(".dl").textContent + "|" + r.querySelector(".dv").textContent; })`, &pills)
	if len(pills) != 3 || !strings.HasPrefix(pills[0], "Measured|Used on this page (approx.)|640 KB") || !strings.HasPrefix(pills[1], "Measured|") || !strings.HasPrefix(pills[2], "Estimate|") || !strings.Contains(pills[2], "about 1.9 MB") {
		t.Fatalf("popover rows: %v", pills)
	}
	var btns []string
	r.chromeEval(`Array.prototype.map.call(document.querySelectorAll(".dpop .dacts .btn"), function(b){ return b.textContent; })`, &btns)
	if len(btns) != 2 || !strings.Contains(btns[0], "Load images on this page (12)") || !strings.Contains(btns[1], "Load fully, once") {
		t.Fatalf("buttons %v", btns)
	}
	// the buttons talk to Go
	r.chromeEval(`document.querySelectorAll(".dpop .dacts .btn")[0].click(); 1`, nil)
	r.waitFor("load images", func() bool { r.f.mu.Lock(); defer r.f.mu.Unlock(); return r.f.images == 1 })
	r.chromeEval(`document.querySelectorAll(".dpop .dseg")[1].querySelectorAll("button")[2].click(); 1`, nil) // Always lite
	r.waitFor("site rule saved", func() bool { return r.app.prof.Settings.DataSites()[store.OriginOf(r.srv.URL)] == "lite" })
}

func TestRealUIHardStopPageButtons(t *testing.T) {
	r := realDSRig(t, Config{})
	mb := int64(store.MB)
	r.setBudget(map[string]any{"enabled": true, "limitMB": 10, "hardStop": true})
	r.f.add(r.defCtx(), 12*mb, nil)
	r.poll()
	r.uiPost("nav.reload", map[string]any{})
	r.waitFor("interstitial", func() bool {
		var n float64
		e := r.app.activePage()
		return e.call(func() error { return e.eval(`document.querySelectorAll(".acts .btn").length`, &n) }) == nil && n == 3
	})
	var text string
	r.pageEval(`document.body.innerText`, &text)
	for _, want := range []string{"You have used your 10 MB this month", "Load this page once", "Raise limit", "Wait until reset", "Loading once still counts toward your total."} {
		if !strings.Contains(text, want) {
			t.Errorf("interstitial lacks %q in %q", want, text)
		}
	}
	// Raise limit opens presets, Raise and load lifts the stop
	r.pageEval(`document.querySelectorAll(".acts .btn")[1].click(); 1`, nil)
	r.idle()
	r.pageEval(`document.body.innerText`, &text)
	if !strings.Contains(text, "New limit") || !strings.Contains(text, "No limit") {
		t.Fatalf("raise panel: %q", text)
	}
	r.pageEval(`document.querySelector(".raise .btn.primary").click(); 1`, nil)
	r.waitFor("page after raising", func() bool { return r.tab().Title == "Page Two" })
	if got := r.app.prof.Settings.Budget().LimitMB; got <= 10 {
		t.Fatalf("limit not raised: %d", got)
	}
	// stop again and use Load once from the real page
	r.setBudget(map[string]any{"limitMB": 10})
	r.uiPost("nav.reload", map[string]any{})
	r.waitFor("stopped", func() bool { return r.tab().Title != "Page Two" })
	r.waitFor("buttons", func() bool {
		var n float64
		e := r.app.activePage()
		return e.call(func() error { return e.eval(`document.querySelectorAll(".acts .btn").length`, &n) }) == nil && n == 3
	})
	r.pageEval(`document.querySelectorAll(".acts .btn")[0].click(); 1`, nil)
	r.waitFor("load once", func() bool { return r.tab().Title == "Page Two" })
}

func TestRealUISettingsDataSection(t *testing.T) {
	r := realDSRig(t, Config{})
	r.uiPost("tab.new", map[string]any{"url": "sweef://settings#data"})
	r.waitFor("settings tab", func() bool { return strings.HasPrefix(r.tab().URL, "sweef://settings") })
	r.waitFor("data section", func() bool {
		var n float64
		e := r.app.activePage()
		return e.call(func() error { return e.eval(`document.querySelectorAll(".lvl").length`, &n) }) == nil && n == 3
	})
	var text string
	r.pageEval(`document.body.innerText`, &text)
	for _, want := range []string{"What it does", "What it does not do", "100 MB", "500 MB", "1 GB", "No limit", "Stop loading pages when the budget is used up", "Measured", "Estimate", "Light loading for Sidekick"} {
		if !strings.Contains(text, want) {
			t.Errorf("settings data lacks %q", want)
		}
	}
	// click the 500 MB preset and the Text first card
	r.pageEval(`document.querySelectorAll(".pchip")[1].click(); document.querySelectorAll(".lvl")[2].click(); 1`, nil)
	r.waitFor("budget saved", func() bool { b := r.app.prof.Settings.Budget(); return b.Enabled && b.LimitMB == 500 })
	r.waitFor("level saved", func() bool { return r.app.prof.Settings.DataLevel() == "strict" })
	r.pageEval(`document.querySelectorAll(".pchip")[3].click(); 1`, nil) // No limit
	r.waitFor("unlimited", func() bool { return !r.app.prof.Settings.Budget().Enabled })
}

func TestRealUIOnboardingPlanAndNewTabLine(t *testing.T) {
	r := realDSRig(t, Config{})
	r.uiPost("tab.new", map[string]any{"url": "sweef://onboarding"})
	r.waitFor("onboarding", func() bool { return strings.HasPrefix(r.tab().URL, "sweef://onboarding") })
	r.pageEval(`window.sweef && sweef.__recv && sweef.__recv("onboarding.step", {n: 3}); 1`, nil)
	r.waitFor("plan cards", func() bool {
		var n float64
		e := r.app.activePage()
		return e.call(func() error { return e.eval(`document.querySelectorAll(".plan").length`, &n) }) == nil && n == 4
	})
	r.pageEval(`document.querySelectorAll(".plan")[0].click(); 1`, nil) // 100 MB
	r.waitFor("100 MB chosen", func() bool {
		return r.app.prof.Settings.Budget().Enabled && r.app.prof.Settings.Budget().LimitMB == 100
	})
	r.waitFor("a 100 MB plan starts in Text first", func() bool { return r.app.prof.Settings.DataLevel() == "strict" })
	r.pageEval(`document.querySelectorAll(".plan")[3].click(); 1`, nil) // no limit
	r.waitFor("no limit", func() bool { return !r.app.prof.Settings.Budget().Enabled })
	r.waitFor("no limit leaves the saver off", func() bool { return r.app.prof.Settings.DataLevel() == "off" })
	// "I am not sure" changes nothing it did not own: back to auto
	r.pageEval(`document.querySelector(".plannote").nextSibling.querySelector("button").click(); 1`, nil)
	r.waitFor("auto", func() bool { return r.app.prof.Settings.Str("data.level") == "auto" })

	// the new tab line
	r.setBudget(map[string]any{"enabled": true, "limitMB": 100})
	r.f.add(r.defCtx(), 39*int64(store.MB), nil)
	r.poll()
	r.uiPost("tab.new", map[string]any{})
	r.waitFor("new tab", func() bool { return r.tab().URL == newTabURL })
	r.waitFor("data line", func() bool {
		var s string
		e := r.app.activePage()
		return e.call(func() error { return e.eval(`document.getElementById("dataline").textContent`, &s) }) == nil && s == "61 MB left this month"
	})
}
