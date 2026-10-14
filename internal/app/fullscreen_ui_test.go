package app

import (
	"strings"
	"testing"
)

// Element fullscreen: the page asks after a real click, the window goes fullscreen and the page gets the whole
// window (the chrome is hidden); Esc leaves, the page sees fullscreenchange and everything is restored.
func TestRealUIElementFullscreen(t *testing.T) {
	h := realMediaRig(t)
	before := h.vp()
	if before.Y < 20 {
		t.Fatalf("no chrome above the page: %+v", before)
	}
	h.pageEval(`window.evs=[]; document.addEventListener('fullscreenchange', function(){ evs.push(document.fullscreenElement ? 'in' : 'out'); }); 1`, nil)
	h.click(before.X+before.W/2, before.Y+before.H/2) // a real gesture
	h.pageEval(`window.fsr='pending'; document.body.requestFullscreen().then(function(){fsr='ok'}, function(e){fsr=e.name}); 1`, nil)
	h.waitFor("fullscreen granted", func() bool {
		var s string
		h.pageEval(`new Promise(function(r){ setTimeout(r, 20); }).then(function(){ return window.fsr; })`, &s) // runs the page's timers
		if s != "ok" && s != "pending" {
			t.Fatalf("requestFullscreen: %s", s)
		}
		return s == "ok"
	})
	h.idle()
	if !h.win.Fullscreen() {
		t.Fatal("the window did not go fullscreen")
	}
	if vp := h.vp(); vp.X != 0 || vp.Y != 0 || int(vp.W) != h.logicalW() {
		t.Fatalf("the page does not have the whole window: %+v", vp)
	}
	// Esc leaves
	h.key("Escape")
	h.waitFor("window restored", func() bool { return !h.win.Fullscreen() })
	if vp := h.vp(); vp != before {
		t.Fatalf("viewport after leaving: %+v, want %+v", vp, before)
	}
	var evs string
	h.pageEval(`evs.join()+'|'+(document.fullscreenElement===null)`, &evs)
	if evs != "in,out|true" {
		t.Fatalf("page events %q", evs)
	}
}

// The page's Media Session shows in the chrome as a "now playing" button whose controls reach the page's handlers.
func TestRealUINowPlaying(t *testing.T) {
	h := realMediaRig(t)
	if n := h.chromeCount("#npbtn:not(.hide)"); n != 0 {
		t.Fatal("now playing shown without a media session")
	}
	h.pageEval(`window.acts=[]; navigator.mediaSession.metadata = new MediaMetadata({title:'Night Drive', artist:'Synth'});
	  navigator.mediaSession.playbackState='playing';
	  ['play','pause','nexttrack'].forEach(function(a){ navigator.mediaSession.setActionHandler(a, function(){ acts.push(a); }); }); 1`, nil)
	h.pageEval(`new Promise(function(r){ setTimeout(r, 20); }).then(function(){ return 1; })`, nil) // lets the page's microtasks and timers run
	h.waitFor("now playing button", func() bool { return h.chromeCount("#npbtn:not(.hide)") == 1 })
	h.chromeClick("#npbtn")
	h.waitFor("controls", func() bool { return h.chromeCount(".nppop .npctl button") == 3 })
	if got := h.chromeText(".nppop .nphead"); !strings.Contains(got, "Night Drive") || !strings.Contains(got, "Synth") {
		t.Fatalf("head %q", got)
	}
	// previous has no handler: disabled; next and pause run the page's handlers
	var dis []bool
	h.chromeEval(`Array.prototype.map.call(document.querySelectorAll('.nppop .npctl button'),function(b){return b.disabled})`, &dis)
	if len(dis) != 3 || !dis[0] || dis[1] || dis[2] {
		t.Fatalf("disabled flags %v", dis)
	}
	h.chromeEval(`document.querySelectorAll('.nppop .npctl button')[2].click(); 1`, nil)
	h.chromeEval(`document.querySelectorAll('.nppop .npctl button')[1].click(); 1`, nil)
	h.waitFor("handlers ran", func() bool {
		var s string
		h.pageEval(`acts.join()`, &s)
		return s == "nexttrack,pause"
	})
}
