package engine

import (
	"runtime"
	"testing"
	"time"
)

// The received track starts muted, unmutes when the first RTP arrives, mutes again when the sender stops, and
// ends when the connection closes.
func TestWebRTCRemoteTrackMuteUnmuteEnded(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, `(function () {
	  window.__swFakeMediaDevices = true; window.log = []; window.err = [];
	  var a = new RTCPeerConnection(), b = new RTCPeerConnection();
	  window.a = a; window.b = b;
	  a.onicecandidate = function (e) { if (e.candidate) b.addIceCandidate(e.candidate); };
	  b.onicecandidate = function (e) { if (e.candidate) a.addIceCandidate(e.candidate); };
	  b.ontrack = function (e) {
	    var t = e.track; window.rt = t;
	    log.push('track muted=' + t.muted);
	    t.onunmute = function () { log.push('unmute'); };
	    t.onmute = function () { log.push('mute'); };
	    t.onended = function () { log.push('ended'); };
	  };
	  navigator.mediaDevices.getUserMedia({ audio: true }).then(function (s) {
	    window.s = s;
	    window.sender = a.addTrack(s.getAudioTracks()[0], s);
	    return a.createOffer();
	  }).then(function (o) { return a.setLocalDescription(o).then(function () { return b.setRemoteDescription(a.localDescription); }); })
	    .then(function () { return b.createAnswer(); })
	    .then(function (an) { return b.setLocalDescription(an).then(function () { return a.setRemoteDescription(b.localDescription); }); })
	    .catch(function (e) { err.push(String(e)); });
	  return 1; })()`, nil)
	waitJS(t, tab, `log.indexOf('unmute') >= 0`, 15*time.Second)
	var log []string
	evalJSON(t, tab, `log`, &log)
	if len(log) < 2 || log[0] != "track muted=true" || log[1] != "unmute" {
		t.Fatalf("event order %v, want the track to arrive muted and then unmute", log)
	}
	// the sender stops sending: the track mutes
	evalJSON(t, tab, `(sender.replaceTrack(null), 1)`, nil)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		pumpFor(tab, 200*time.Millisecond)
		var muted bool
		evalJSON(t, tab, `rt.muted`, &muted)
		if muted {
			break
		}
	}
	evalJSON(t, tab, `log`, &log)
	if log[len(log)-1] != "mute" {
		t.Errorf("the track did not mute when the sender stopped: %v", log)
	}
	// and comes back
	evalJSON(t, tab, `(sender.replaceTrack(s.getAudioTracks()[0]), 1)`, nil)
	waitJS(t, tab, `rt.muted === false`, 8*time.Second)
	evalJSON(t, tab, `(b.close(), 1)`, nil)
	pumpFor(tab, 200*time.Millisecond)
	evalJSON(t, tab, `log`, &log)
	if log[len(log)-1] != "ended" {
		t.Errorf("no ended event after close: %v", log)
	}
}

// Repeated calls must not accumulate goroutines or frames.
func TestWebRTCCallsDoNotLeak(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	base := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		evalJSON(t, tab, callJS(320, 240, 30), nil)
		waitJS(t, tab, `rv.readyState >= 2`, 20*time.Second)
		pumpFor(tab, 500*time.Millisecond)
		evalJSON(t, tab, `(local.getTracks().forEach(function (t) { t.stop(); }), a.close(), b.close(), rv.srcObject = null, rv.remove(), 1)`, nil)
		pumpFor(tab, 300*time.Millisecond)
	}
	var n int
	for k := 0; k < 50; k++ {
		n = runtime.NumGoroutine()
		if n <= base+4 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("goroutines: %d before three calls, %d after", base, n)
	if n > base+4 {
		buf := make([]byte, 1<<16)
		m := runtime.Stack(buf, true)
		t.Errorf("goroutines grew from %d to %d:\n%s", base, n, buf[:m])
	}
}

// Data Saver strict still allows a call but caps the video bitrate.
func TestWebRTCDataSaverStrictCapsVideo(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	p := PolicyFor(SaverStrict)
	tab.SetDataPolicy(&p)
	evalJSON(t, tab, callJS(640, 360, 30), nil)
	waitJS(t, tab, `rv.readyState >= 2`, 20*time.Second)
	pumpFor(tab, 6*time.Second)
	evalJSON(t, tab, `(window.st = null, a.getStats().then(function (rep) { var o = {}; rep.forEach(function (s, id) { o[id] = s; }); window.st = o; }), 1)`, nil)
	waitJS(t, tab, `st !== null`, 5*time.Second)
	var st map[string]map[string]any
	evalJSON(t, tab, `st`, &st)
	for _, s := range st {
		if s["type"] == "outbound-rtp" && s["kind"] == "video" {
			target, _ := s["targetBitrate"].(float64)
			bytes, _ := s["bytesSent"].(float64)
			t.Logf("strict: targetBitrate %.0f, %.0f bytes sent in about 7 s", target, bytes)
			if target > 300_000 {
				t.Errorf("strict level did not cap the video bitrate: %.0f", target)
			}
			if bytes < 10000 {
				t.Errorf("no video was sent at strict level")
			}
		}
	}
}
