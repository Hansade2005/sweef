package engine

import (
	"testing"
	"time"
)

// The Fullscreen API: it needs transient user activation, marks the element, tells the app (EvFullscreen) and
// the page sees fullscreenchange on entry and on a host-side exit (Esc).
func TestFullscreenAPI(t *testing.T) {
	tab := platformTab(t)
	_, ch := tab.eng.Bus().Subscribe(64)
	var r map[string]any
	evalJSON(t, tab, `(async function(){
	  var out = {};
	  var d = document.createElement('div'); d.id='fs'; document.body.appendChild(d);
	  out.enabled = document.fullscreenEnabled; out.before = document.fullscreenElement === null;
	  out.api = [typeof d.requestFullscreen, typeof d.webkitRequestFullscreen, typeof document.exitFullscreen, typeof document.webkitExitFullscreen].join();
	  try { await d.requestFullscreen(); out.noGesture = 'resolved'; } catch (e) { out.noGesture = e.name; }
	  window.evs = []; document.addEventListener('fullscreenchange', function(){ evs.push(document.fullscreenElement && document.fullscreenElement.id || 'none'); });
	  return out; })()`, &r)
	if r["enabled"] != true || r["before"] != true || r["noGesture"] != "TypeError" || r["api"] != "function,function,function,function" {
		t.Fatalf("before the gesture: %v", r)
	}
	// a real click grants transient activation
	_, _ = tab.Evaluate(`document.body.insertAdjacentHTML('beforeend','<button id="b">go</button>');1`)
	if err := tab.Click("#b"); err != nil {
		t.Fatal(err)
	}
	evalJSON(t, tab, `(async function(){ await document.getElementById('fs').requestFullscreen(); return 1; })()`, nil)
	tab.Settle(30 * time.Millisecond)
	evalJSON(t, tab, `({el: document.fullscreenElement && document.fullscreenElement.id, wk: document.webkitFullscreenElement === document.fullscreenElement, evs: evs.join(), match: document.getElementById('fs').matches(':fullscreen')})`, &r)
	if r["el"] != "fs" || r["wk"] != true || r["evs"] != "fs" || r["match"] != true {
		t.Fatalf("after requestFullscreen: %v", r)
	}
	if !tab.Fullscreen().Active {
		t.Error("the tab does not report fullscreen")
	}
	// the element covers the viewport
	evalJSON(t, tab, `(function(){var b=document.getElementById('fs').getBoundingClientRect();return {w:b.width===innerWidth,h:b.height===innerHeight,x:b.left,y:b.top}})()`, &r)
	if r["w"] != true || r["h"] != true || r["x"] != 0.0 || r["y"] != 0.0 {
		t.Errorf("the fullscreen element does not fill the viewport: %v", r)
	}
	sawOn := false
drain:
	for {
		select {
		case ev := <-ch:
			if st, ok := ev.Data.(FullscreenState); ev.Kind == EvFullscreen && ok && st.Active {
				sawOn = true
			}
		default:
			break drain
		}
	}
	if !sawOn {
		t.Error("no EvFullscreen{Active} on the bus")
	}
	// the user presses Esc: the host leaves, the page is told
	tab.ExitFullscreen()
	tab.Settle(30 * time.Millisecond)
	evalJSON(t, tab, `({el: document.fullscreenElement, evs: evs.join(), match: document.getElementById('fs').matches(':fullscreen')})`, &r)
	if r["el"] != nil || r["evs"] != "fs,none" || r["match"] != false {
		t.Fatalf("after the host exit: %v", r)
	}
	if tab.Fullscreen().Active {
		t.Error("still fullscreen")
	}
	var msg string
	evalJSON(t, tab, `document.exitFullscreen().then(function(){return 'ok'}, function(e){return e.name})`, &msg)
	if msg != "TypeError" {
		t.Errorf("exitFullscreen with nothing fullscreen: %q", msg)
	}
}
