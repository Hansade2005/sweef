package app

import (
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// These run the real chrome UI: the prompt, the indicators and the popover are drawn by internal/ui/web/chrome.

func realMediaRig(t *testing.T) *harness {
	t.Helper()
	t.Setenv("SWEEF_FAKE_DEVICES", "1")
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Home Page")
	h.waitUIReady()
	return h
}

func (h *harness) chromeText(sel string) (s string) {
	h.t.Helper()
	h.chromeEval(`(function(){var e=document.querySelector(`+jsString(sel)+`);return e?e.textContent:null})()`, &s)
	return
}

func (h *harness) chromeCount(sel string) (n float64) {
	h.t.Helper()
	h.chromeEval(`document.querySelectorAll(`+jsString(sel)+`).length`, &n)
	return
}

func (h *harness) chromeClick(sel string) {
	h.t.Helper()
	h.chromeEval(`document.querySelector(`+jsString(sel)+`).click(); 1`, nil)
	h.idle()
}

func (h *harness) buttonTexts(root string) (out []string) {
	h.t.Helper()
	h.chromeEval(`Array.prototype.map.call(document.querySelectorAll(`+jsString(root+" button")+`),function(b){return b.textContent})`, &out)
	return
}

func TestRealUIMediaPromptAndIndicators(t *testing.T) {
	h := realMediaRig(t)
	h.gum(`{video:true,audio:true}`)
	h.waitFor("the prompt is drawn", func() bool { return h.chromeCount(".mprompt") == 1 })
	h.idle()
	if got := h.chromeText(".mprompt .mt"); !strings.Contains(got, "wants to use your camera and microphone") || !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("title %q", got)
	}
	btns := h.buttonTexts(".mprompt")
	want := []string{"Allow this time", "Always allow on this site", "Block"}
	var seen []string
	for _, b := range btns {
		for _, w := range want {
			if b == w {
				seen = append(seen, b)
			}
		}
	}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("buttons %v", btns)
	}
	if got := h.chromeText(".mprompt .mdev .mrow"); !strings.Contains(got, "Fake Camera") {
		t.Fatalf("camera row %q", got)
	}
	// the prompt sits below the toolbar, over the page, and takes pointer input
	var geo map[string]float64
	h.chromeEval(`(function(){var r=document.querySelector('.mprompt').getBoundingClientRect();return {x:r.left,y:r.top,w:r.width,h:r.height}})()`, &geo)
	if geo["y"] < 60 || geo["w"] < 300 || geo["x"]+geo["w"] > 1280 {
		t.Fatalf("prompt geometry %v", geo)
	}
	if tgt := h.targetAt(geo["x"]+40, geo["y"]+geo["h"]-20); tgt != "chrome" {
		t.Fatalf("a click on the prompt would go to %q", tgt)
	}
	// the page is still waiting
	if h.result() != "pending" {
		t.Fatal("resolved early")
	}
	// answer with the real button
	h.chromeEval(`Array.prototype.filter.call(document.querySelectorAll('.mprompt button'),function(b){return b.textContent==='Allow this time'})[0].click(); 1`, nil)
	h.idle()
	h.waitResult("ok")
	h.waitFor("prompt gone", func() bool { return h.chromeCount(".mprompt") == 0 })
	// indicators: tab strip icon with a red dot, toolbar button
	h.waitFor("tab indicator", func() bool { return h.chromeCount(".tab .md.rec") == 1 })
	if lbl := ""; true {
		h.chromeEval(`document.querySelector('.tab .md').getAttribute('aria-label')`, &lbl)
		if lbl != "Camera and microphone in use" {
			t.Fatalf("tab indicator label %q", lbl)
		}
	}
	h.waitFor("toolbar indicator", func() bool { return h.chromeCount("#mediabtn:not(.hide)") == 1 })
	// the popover lists both sessions and stops one
	h.chromeClick("#mediabtn")
	h.waitFor("popover", func() bool { return h.chromeCount(".mpop .mses") == 2 })
	h.chromeEval(`document.querySelector('.mpop .mses .btn').click(); 1`, nil)
	h.waitFor("one session stopped", func() bool { return h.chromeCount(".mpop .mses") == 1 })
	// site rows are there to revoke
	h.chromeEval(`Array.prototype.filter.call(document.querySelectorAll('.mpop .mperm'),function(r){return r.textContent.indexOf('Camera')===0})[0].querySelectorAll('button')[2].click(); 1`, nil)
	h.idle()
	if got := h.app.prof.Sites.Media(h.srvOrigin(), "camera"); got != "block" {
		t.Fatalf("blocking from the popover stored %q", got)
	}
}

func (h *harness) srvOrigin() string {
	t := h.tab()
	i := strings.Index(t.URL[8:], "/")
	if strings.HasPrefix(t.URL, "http://") {
		if j := strings.Index(t.URL[7:], "/"); j >= 0 {
			return t.URL[:7+j]
		}
		return t.URL
	}
	_ = i
	return t.URL
}

func TestRealUIMediaPromptKeyboardAndDismiss(t *testing.T) {
	h := realMediaRig(t)
	h.gum(`{audio:true}`)
	h.waitFor("the prompt is drawn", func() bool { return h.chromeCount(".mprompt") == 1 })
	h.idle()
	// private note is absent for a normal window; the bubble has a dialog role and a label
	var attrs map[string]string
	h.chromeEval(`(function(){var e=document.querySelector('.mprompt');return {role:e.getAttribute('role'),label:e.getAttribute('aria-label'),modal:e.getAttribute('aria-modal'),tab:e.getAttribute('tabindex')}})()`, &attrs)
	if attrs["role"] != "dialog" || attrs["modal"] != "false" || !strings.Contains(attrs["label"], "microphone") {
		t.Fatalf("a11y attributes %v", attrs)
	}
	// typing into the page must not answer the prompt: Enter and Space went nowhere
	h.key("enter")
	h.key("space")
	if h.chromeCount(".mprompt") != 1 || h.result() != "pending" {
		t.Fatal("a key press answered the prompt")
	}
	// Escape dismisses it when the chrome has focus
	h.chromeEval(`document.querySelector('.mprompt').dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true})); 1`, nil)
	h.idle()
	h.waitResult("NotAllowedError:Permission dismissed")
	h.waitFor("prompt gone", func() bool { return h.chromeCount(".mprompt") == 0 })
	// a dismissal stored nothing
	if len(h.app.MediaOrigins()) != 0 {
		t.Fatalf("stored %v", h.app.MediaOrigins())
	}
}

func TestRealUIDisplayChooserAndShareBar(t *testing.T) {
	h := realMediaRig(t)
	h.pageEval(`window.r='pending'; navigator.mediaDevices.getDisplayMedia({video:true}).then(function(s){window.s=s;window.r='ok'},function(e){window.r=e.name}); 1`, nil)
	h.waitFor("chooser", func() bool { return h.chromeCount(".mprompt .msel") == 2 })
	if got := h.chromeText(".mprompt .mt"); !strings.Contains(got, "wants to share your screen content") {
		t.Fatalf("title %q", got)
	}
	if got := h.chromeCount(".mprompt .msel[aria-checked=true]"); got != 1 {
		t.Fatalf("%v options pre-selected", got)
	}
	if got := h.chromeText(".mprompt .mnote"); !strings.Contains(got, "whole screen are not available") {
		t.Fatalf("the OS-capture limit must be stated: %q", got)
	}
	h.chromeEval(`Array.prototype.filter.call(document.querySelectorAll('.mprompt .msel'),function(b){return b.textContent.indexOf('window')>=0})[0].click(); 1`, nil)
	h.idle()
	h.chromeEval(`Array.prototype.filter.call(document.querySelectorAll('.mprompt button'),function(b){return b.textContent==='Share'})[0].click(); 1`, nil)
	h.idle()
	h.waitResult("ok")
	h.waitFor("share bar", func() bool { return h.chromeCount("#sharebar:not(.hide)") == 1 })
	if got := h.chromeText("#sharebar .t"); !strings.Contains(got, "is sharing this window") {
		t.Fatalf("bar text %q", got)
	}
	h.waitFor("tab sharing icon", func() bool { return h.chromeCount(".tab .md.rec") == 1 })
	// Stop sharing ends the track and removes the bar
	h.pageEval(`window.ended=false; s.getVideoTracks()[0].onended=function(){ended=true}; 1`, nil)
	h.chromeEval(`document.querySelector('#sharebar .btn').click(); 1`, nil)
	h.idle()
	h.waitFor("page sees ended", func() bool { var b bool; h.pageEval(`ended`, &b); return b })
	h.waitFor("bar hidden", func() bool { return h.chromeCount("#sharebar.hide") == 1 })
	if capture.LiveTracks() != 0 {
		t.Fatalf("%d tracks live", capture.LiveTracks())
	}
}

func TestRealUISiteInfoRevoke(t *testing.T) {
	h := realMediaRig(t)
	h.app.prof.Sites.SetMedia(h.srvOrigin(), "camera", "allow")
	h.app.prof.Sites.SetMedia(h.srvOrigin(), "microphone", "block")
	h.loop(func() { h.app.markState() })
	h.idle()
	h.chromeClick("#secbtn")
	h.waitFor("site info", func() bool { return h.chromeCount(".permrow") == 2 })
	if got := h.chromeText(".permrow"); !strings.Contains(got, "Camera") || !strings.Contains(got, "Allowed") {
		t.Fatalf("row %q", got)
	}
	h.chromeEval(`document.querySelector('.permrow button').click(); 1`, nil)
	h.idle()
	if got := h.app.prof.Sites.Media(h.srvOrigin(), "camera"); got != "" {
		t.Fatalf("reset left %q", got)
	}
	if got := h.app.prof.Sites.Media(h.srvOrigin(), "microphone"); got != "block" {
		t.Fatalf("the other decision changed: %q", got)
	}
}
