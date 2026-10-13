package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/capture"
)

func mediaRig(t *testing.T, cfg Config) (*harness, *httptest.Server) {
	t.Helper()
	t.Setenv("SWEEF_FAKE_DEVICES", "1")
	srv := pageServer(t)
	cfg.InitialURLs = []string{srv.URL + "/"}
	h := newHarness(t, hopts{cfg: cfg})
	h.waitTitle("Home Page")
	return h, srv
}

// gum starts getUserMedia in the page without waiting for it and returns once the call is made.
func (h *harness) gum(c string) {
	h.t.Helper()
	h.pageEval(`window.r='pending'; navigator.mediaDevices.getUserMedia(`+c+`).then(function(s){window.s=s;window.r='ok'},function(e){window.r=e.name+':'+e.message}); 1`, nil)
}

func (h *harness) result() (s string) {
	h.t.Helper()
	h.pageEval(`window.r`, &s)
	return
}

func (h *harness) waitResult(want string) {
	h.t.Helper()
	h.waitFor("page result "+want, func() bool { return h.result() == want })
}

func (h *harness) prompt() (p map[string]any) {
	h.t.Helper()
	h.loop(func() {
		m, _ := h.app.lastState["media"].(map[string]any)
		p, _ = m["prompt"].(map[string]any)
	})
	return
}

func (h *harness) waitPrompt() map[string]any {
	h.t.Helper()
	var p map[string]any
	h.waitFor("a media prompt in the state", func() bool { p = h.prompt(); return p != nil })
	return p
}

func (h *harness) answer(p map[string]any, action string) {
	h.t.Helper()
	h.loop(func() { h.app.media.answer(p["id"].(string), action, "", "", "") })
	h.idle()
}

func (h *harness) mediaFlags() (cam, mic, share bool) {
	h.t.Helper()
	h.loop(func() {
		t, _ := h.app.model.Tab(h.app.model.Active(h.app.winID))
		cam, mic, share = t.Cam, t.Mic, t.Share
	})
	return
}

func TestMediaPromptOnce(t *testing.T) {
	h, srv := mediaRig(t, Config{})
	h.gum(`{video:true,audio:true}`)
	p := h.waitPrompt()
	if p["kind"] != "devices" || p["video"] != true || p["audio"] != true || p["origin"] != srv.URL || p["remember"] != true || p["incognito"] != false {
		t.Fatalf("prompt %v", p)
	}
	if c, _ := p["cameras"].([]any); len(c) != 1 || c[0].(map[string]any)["label"] != "Fake Camera" {
		t.Fatalf("cameras %v", p["cameras"])
	}
	if h.result() != "pending" {
		t.Fatal("getUserMedia resolved before the user answered")
	}
	h.answer(p, "once")
	h.waitResult("ok")
	if v := h.app.prof.Sites.Media(srv.URL, "camera"); v != "" {
		t.Fatalf("\"Allow this time\" stored %q", v)
	}
	h.waitFor("tab flags", func() bool { c, m, _ := h.mediaFlags(); return c && m })
	// the tab strip data carries the indicators
	h.waitFor("indicators in the state", func() bool {
		for _, x := range h.stateTabs() {
			tb := x.(map[string]any)
			if tb["id"] == h.tab().ID && tb["camera"] == true && tb["mic"] == true {
				return true
			}
		}
		return false
	})
	if h.prompt() != nil {
		t.Fatal("prompt still shown after the answer")
	}
	// asking again prompts again: nothing was remembered
	h.gum(`{video:true}`)
	p = h.waitPrompt()
	h.answer(p, "dismiss")
	h.waitResult("NotAllowedError:Permission dismissed")
	// stopping the tracks clears the indicators
	h.pageEval(`window.s.getTracks().forEach(function(t){t.stop()}); 1`, nil)
	h.waitFor("indicators cleared", func() bool { c, m, _ := h.mediaFlags(); return !c && !m })
	if capture.LiveTracks() != 0 {
		t.Fatalf("%d tracks live", capture.LiveTracks())
	}
}

func TestMediaAlwaysAndBlockPersist(t *testing.T) {
	dir := t.TempDir()
	prof, err := store.OpenProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, srv := mediaRig(t, Config{Profile: prof, NoRestore: true})
	h.gum(`{video:true}`)
	p := h.waitPrompt()
	h.answer(p, "always")
	h.waitResult("ok")
	if prof.Sites.Media(srv.URL, "camera") != "allow" || prof.Sites.Media(srv.URL, "microphone") != "" {
		t.Fatalf("stored rule: camera=%q mic=%q", prof.Sites.Media(srv.URL, "camera"), prof.Sites.Media(srv.URL, "microphone"))
	}
	// the next request does not prompt, and the stored decision survives a reopen of the profile
	h.pageEval(`window.s.getTracks().forEach(function(t){t.stop()}); 1`, nil)
	h.gum(`{video:true}`)
	h.waitResult("ok")
	if h.prompt() != nil {
		t.Fatal("prompted although camera is allowed")
	}
	re, err := store.OpenProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if re.Sites.Media(srv.URL, "camera") != "allow" {
		t.Fatal("the decision was not persisted to the profile")
	}
	if re.Sites.Salt() == "" || re.Sites.Salt() != prof.Sites.Salt() {
		t.Fatal("the device-id salt must be stable across restarts")
	}
	// the microphone is still asked
	h.gum(`{audio:true}`)
	p = h.waitPrompt()
	h.answer(p, "block")
	h.waitResult("NotAllowedError:Permission denied")
	if prof.Sites.Media(srv.URL, "microphone") != "block" {
		t.Fatal("Block was not stored")
	}
	h.gum(`{audio:true}`)
	h.waitResult("NotAllowedError:Permission denied")
	if h.prompt() != nil {
		t.Fatal("prompted although the microphone is blocked")
	}
	// site settings: reset to "ask" and the prompt returns
	h.uiPostApp("site.media", map[string]any{"origin": srv.URL, "name": "microphone", "value": ""})
	h.gum(`{audio:true}`)
	p = h.waitPrompt()
	h.answer(p, "once")
	h.waitResult("ok")
}

// uiPostApp delivers a chrome message straight to the app (the placeholder chrome has no sweef.post).
func (h *harness) uiPostApp(typ string, payload any) {
	h.t.Helper()
	raw, _ := json.Marshal(payload)
	h.loop(func() { h.app.onUIMessage(h.app.chromeTab(), typ, raw) })
	h.idle()
}

func TestMediaRevokeEndsLiveCapture(t *testing.T) {
	h, srv := mediaRig(t, Config{})
	h.pageEval(`window.log=[]; navigator.permissions.query({name:'camera'}).then(function(p){window.ps=p;p.onchange=function(){log.push(p.state)}}); 1`, nil)
	h.gum(`{video:true,audio:true}`)
	h.answer(h.waitPrompt(), "always")
	h.waitResult("ok")
	h.pageEval(`window.ended=[]; s.getTracks().forEach(function(t){t.onended=function(){ended.push(t.kind)}}); 1`, nil)
	// revoke the camera in the site-info popover while it is live
	h.uiPostApp("site.media", map[string]any{"origin": srv.URL, "name": "camera", "value": "block"})
	h.waitFor("track ended", func() bool {
		var n float64
		h.pageEval(`ended.length`, &n)
		return n == 1
	})
	var res map[string]any
	h.pageEval(`({v:s.getVideoTracks()[0].readyState,a:s.getAudioTracks()[0].readyState,state:ps.state,log:log})`, &res)
	if res["v"] != "ended" || res["a"] != "live" || res["state"] != "denied" {
		t.Fatalf("%v", res)
	}
	h.waitFor("camera indicator off", func() bool { c, m, _ := h.mediaFlags(); return !c && m })
}

func TestMediaIncognitoNeverPersists(t *testing.T) {
	dir := t.TempDir()
	prof, err := store.OpenProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, srv := mediaRig(t, Config{Profile: prof, NoRestore: true})
	h.app.post(func() { h.app.newTab(srv.URL+"/", true, true) })
	h.waitFor("incognito tab", func() bool { return h.tab().Incognito && h.tab().Title == "Home Page" })
	h.gum(`{video:true}`)
	p := h.waitPrompt()
	if p["incognito"] != true || p["remember"] != false {
		t.Fatalf("a private window must not offer to remember: %v", p)
	}
	// even a forged "always" is kept in memory only
	h.answer(p, "always")
	h.waitResult("ok")
	if prof.Sites.Media(srv.URL, "camera") != "" {
		t.Fatal("a private tab wrote a decision to the regular profile")
	}
	re, _ := store.OpenProfile(dir)
	if re.Sites.Media(srv.URL, "camera") != "" || len(re.Sites.MediaOrigins()) != 0 {
		t.Fatal("a private decision reached the disk")
	}
	// the private tab sees decisions of the regular profile (read-only)
	prof.Sites.SetMedia(srv.URL, "microphone", "block")
	h.gum(`{audio:true}`)
	h.waitResult("NotAllowedError:Permission denied")
}

func TestMediaDisplayChooserAndTabCapture(t *testing.T) {
	h, srv := mediaRig(t, Config{})
	// a second tab so the chooser has something to list
	h.app.post(func() { h.app.newTab(srv.URL+"/two", false, false) })
	h.waitFor("second tab", func() bool { return len(h.tabIDs()) == 2 })
	h.pageEval(`window.r='pending'; navigator.mediaDevices.getDisplayMedia({video:{width:{max:320},frameRate:{ideal:10}},audio:false}).then(function(s){window.s=s;window.r='ok'},function(e){window.r=e.name+':'+e.message}); 1`, nil)
	p := h.waitPrompt()
	if p["kind"] != "display" {
		t.Fatalf("%v", p)
	}
	srcs := p["sources"].([]any)
	if len(srcs) != 3 {
		t.Fatalf("sources: %v", srcs)
	}
	var cur, win int
	for _, s := range srcs {
		m := s.(map[string]any)
		if m["current"] == true {
			cur++
			if m["surface"] != "browser" || m["title"] != "Home Page" {
				t.Fatalf("current tab entry %v", m)
			}
		}
		if m["surface"] == "window" {
			win++
		}
	}
	if cur != 1 || win != 1 {
		t.Fatalf("current=%d window=%d in %v", cur, win, srcs)
	}
	cid := h.tabIDs()[0]
	h.loop(func() { h.app.media.answer(p["id"].(string), "once", "", "", "tab:"+cid) })
	h.waitResult("ok")
	var st map[string]any
	h.pageEval(`s.getVideoTracks()[0].getSettings()`, &st)
	if st["displaySurface"] != "browser" || st["width"].(float64) > 320 || st["frameRate"].(float64) != 10 {
		t.Fatalf("settings %v", st)
	}
	h.waitFor("sharing indicator", func() bool { _, _, s := h.mediaFlags(); return s })
	// the frames are the tab's pixels: the red banner at the top of the page
	var id string
	h.pageEval(`s.getVideoTracks()[0].id`, &id)
	tr := capture.LookupTrack(id)
	frames, cancel := tr.SubscribeVideo()
	defer cancel()
	var f *capture.Frame
	for i := 0; i < 5; i++ {
		f = <-frames
	}
	rgba := f.ToRGBA()
	px := rgba.RGBAAt(rgba.Bounds().Dx()/2, 4)
	if px.R < 170 || px.G > 90 || px.B > 90 {
		t.Fatalf("top of the shared tab is %v, want the page's red banner (#d33a3a)", px)
	}
	// the chrome's popovers are not part of a tab share: the frame has the viewport's aspect
	vp := h.app.comp.Viewport()
	if want := float64(vp.Dx()) / float64(vp.Dy()); abs64(float64(f.W)/float64(f.H)-want) > 0.02 {
		t.Fatalf("frame %dx%d does not match the viewport %v", f.W, f.H, vp)
	}
	// "Stop sharing"
	sess := h.sessionIDs()
	if len(sess) != 1 {
		t.Fatalf("sessions %v", sess)
	}
	h.pageEval(`window.ended=false; s.getVideoTracks()[0].onended=function(){ended=true}; 1`, nil)
	h.uiPostApp("media.stop", map[string]any{"id": sess[0]})
	h.waitFor("page sees ended", func() bool { var b bool; h.pageEval(`ended`, &b); return b })
	h.waitFor("indicator off", func() bool { _, _, s := h.mediaFlags(); return !s })
}

func abs64(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func (h *harness) sessionIDs() (ids []string) {
	h.t.Helper()
	h.loop(func() {
		m, _ := h.app.lastState["media"].(map[string]any)
		for _, s := range m["sessions"].([]any) {
			ids = append(ids, s.(map[string]any)["id"].(string))
		}
	})
	return
}

func TestMediaWindowAndBackgroundTabCapture(t *testing.T) {
	h, srv := mediaRig(t, Config{})
	h.app.post(func() { h.app.newTab(srv.URL+"/two", false, false) })
	h.waitFor("second tab", func() bool { return len(h.tabIDs()) == 2 })
	first, second := h.tabIDs()[0], h.tabIDs()[1]
	run := func(src string) *capture.Frame {
		h.pageEval(`window.r='pending'; navigator.mediaDevices.getDisplayMedia({video:true}).then(function(s){window.s=s;window.r='ok'},function(e){window.r=e.name}); 1`, nil)
		p := h.waitPrompt()
		h.loop(func() { h.app.media.answer(p["id"].(string), "once", "", "", src) })
		h.waitResult("ok")
		var id string
		h.pageEval(`s.getVideoTracks()[0].id`, &id)
		frames, cancel := capture.LookupTrack(id).SubscribeVideo()
		defer cancel()
		var f *capture.Frame
		for i := 0; i < 4; i++ {
			f = <-frames
		}
		h.pageEval(`s.getTracks().forEach(function(t){t.stop()}); 1`, nil)
		return f
	}
	// the whole window includes the chrome around the page
	w := run(windowSourceID)
	ww, wh, _ := h.app.comp.Size()
	if w.W != ww&^1 || w.H != wh&^1 {
		t.Fatalf("window frame %dx%d, window %dx%d", w.W, w.H, ww, wh)
	}
	// the other tab is not on screen: it is painted on its own (green banner, not the red one of the active tab)
	if first != h.app.model.Active(h.app.winID) {
		t.Fatal("the first tab should still be active")
	}
	f := run("tab:" + second)
	rgba := f.ToRGBA()
	px := rgba.RGBAAt(rgba.Bounds().Dx()/2, 10)
	if px.G < 130 || px.R > 90 {
		t.Fatalf("background tab frame starts with %v, want the page's green (#2f9e44)", px)
	}
	// a share of a tab that is closed meanwhile ends
	h.pageEval(`window.r='pending'; navigator.mediaDevices.getDisplayMedia({video:true}).then(function(s){window.s=s;window.r='ok'},function(e){window.r=e.name}); 1`, nil)
	p := h.waitPrompt()
	h.loop(func() { h.app.media.answer(p["id"].(string), "once", "", "", "tab:"+second) })
	h.waitResult("ok")
	h.pageEval(`window.ended=false; s.getVideoTracks()[0].onended=function(){ended=true}; 1`, nil)
	h.app.post(func() { h.app.closeTab(second) })
	h.waitFor("share of a closed tab ends", func() bool { var b bool; h.pageEval(`ended`, &b); return b })
}

func TestMediaClosingTabEndsCaptureAndCancelsPrompt(t *testing.T) {
	h, srv := mediaRig(t, Config{})
	h.app.post(func() { h.app.newTab(srv.URL+"/two", false, true) })
	h.waitTitle("Page Two")
	h.gum(`{video:true}`)
	p := h.waitPrompt()
	_ = p
	h.app.post(func() { h.app.closeTab(h.app.model.Active(h.app.winID)) })
	h.waitFor("pending prompt dropped with its tab", func() bool { return h.prompt() == nil })
	h.waitTitle("Home Page")
	h.gum(`{video:true}`)
	h.answer(h.waitPrompt(), "once")
	h.waitResult("ok")
	if capture.LiveTracks() != 1 {
		t.Fatalf("live tracks %d", capture.LiveTracks())
	}
	h.app.post(func() { h.app.newTab(srv.URL+"/two", false, true) })
	h.waitTitle("Page Two")
	h.app.post(func() { h.app.closeTab(h.tabIDs()[0]) })
	h.waitFor("camera released when its tab closed", func() bool { return capture.LiveTracks() == 0 })
}

func TestMediaPromptQueueAcrossTabs(t *testing.T) {
	h, srv := mediaRig(t, Config{})
	first := h.tab().ID
	h.gum(`{video:true}`)
	h.waitPrompt()
	h.app.post(func() { h.app.newTab(srv.URL+"/two", false, true) })
	h.waitTitle("Page Two")
	if h.prompt() != nil {
		t.Fatal("tab A's prompt is showing on tab B")
	}
	h.app.post(func() { h.app.activateTab(first) })
	h.waitFor("prompt back on tab A", func() bool { return h.prompt() != nil })
}

func TestMediaOpenCloseNoLeaks(t *testing.T) {
	h, _ := mediaRig(t, Config{})
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		h.gum(`{video:true,audio:true}`)
		h.answer(h.waitPrompt(), "once")
		h.waitResult("ok")
		h.pageEval(`window.s.getTracks().forEach(function(t){t.stop()}); 1`, nil)
	}
	h.waitFor("tracks released", func() bool { return capture.LiveTracks() == 0 })
	h.idle()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > before+8 {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+8 {
		t.Fatalf("goroutines %d -> %d", before, n)
	}
}

func TestStoredRulesJSONShape(t *testing.T) {
	s := store.OpenSites(store.NewMemBackend())
	s.SetMedia("https://a.example/page", "camera", "allow")
	s.SetMedia("https://a.example", "screen", "allow") // screen sharing is never remembered as allowed
	s.SetMedia("https://a.example", "screen", "block")
	s.SetMedia("https://a.example", "bogus", "block")
	s.SetMedia("https://a.example", "microphone", "maybe")
	r := s.Rule("https://a.example")
	if r.Camera != "allow" || r.Screen != "block" || r.Microphone != "" {
		t.Fatalf("%+v", r)
	}
	s.SetMedia("https://a.example", "camera", "")
	s.SetMedia("https://a.example", "screen", "")
	if len(s.All()) != 0 {
		t.Fatalf("an all-default rule must be dropped: %v", s.All())
	}
	_ = fmt.Sprint
	_ = http.StatusOK
	_ = strings.TrimSpace
}
