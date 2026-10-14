package engine

import (
	"testing"
	"time"
)

// The camera a page gets from getUserMedia (internal/capture's fake device, no test-pattern override) travels
// through RTCPeerConnection and shows up in the remote <video>: this is the whole path a real call takes,
// from the capture track to the decoded picture.
func TestWebRTCSendsTheCaptureTrack(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	SetRTCTrackResolver(nil) // use the capture track itself
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, callJS(640, 360, 30), nil)
	waitJS(t, tab, `rv.readyState >= 2 && rv.videoWidth > 0 && rv.videoHeight > 0`, 20*time.Second)
	var w, h int
	evalJSON(t, tab, `rv.videoWidth`, &w)
	evalJSON(t, tab, `rv.videoHeight`, &h)
	if w != 640 || h != 360 {
		t.Fatalf("remote picture is %dx%d, want 640x360", w, h)
	}
	var errs []string
	evalJSON(t, tab, `err`, &errs)
	if len(errs) > 0 {
		t.Fatalf("call errors: %v", errs)
	}
}
