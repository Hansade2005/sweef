package engine

import (
	"testing"
	"time"
)

// Random loss of 5% must be repaired by NACK retransmissions (RTX): the picture keeps moving and no key
// frame storm is needed.
func TestWebRTCNackRepairsRandomLoss(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	fl := newFrameLog()
	fl.install(t)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, callJS(640, 360, 30), nil)
	waitJS(t, tab, `rv.readyState >= 2`, 20*time.Second)
	pumpFor(tab, 2*time.Second)
	rtcSim.set(0.05, 0, 0)
	t.Cleanup(rtcSim.reset)
	n0, _, _ := fl.snapshot()
	pumpFor(tab, 8*time.Second)
	n1, _, _ := fl.snapshot()
	evalJSON(t, tab, `(window.st = null, Promise.all([a.getStats(), b.getStats()]).then(function (r) { window.st = r.map(function (rep) { var o = {}; rep.forEach(function (s, id) { o[id] = s; }); return o; }); }), 1)`, nil)
	waitJS(t, tab, `st !== null`, 5*time.Second)
	var st []map[string]map[string]any
	evalJSON(t, tab, `st`, &st)
	var keyFrames, retrans, nacks, plis float64
	for _, s := range st[0] {
		if s["type"] == "outbound-rtp" && s["kind"] == "video" {
			keyFrames, _ = s["keyFramesEncoded"].(float64)
			retrans, _ = s["retransmittedPacketsSent"].(float64)
			nacks, _ = s["nackCount"].(float64)
			plis, _ = s["pliCount"].(float64)
		}
	}
	fps := float64(n1-n0) / 8
	t.Logf("5%% loss: %.1f fps shown, dropped datagrams %d, sender: nackCount %.0f retransmitted %.0f PLI %.0f key frames %.0f", fps, rtcSim.Dropped, nacks, retrans, plis, keyFrames)
	if fps < 24 {
		t.Errorf("only %.1f fps through 5%% loss", fps)
	}
	if retrans == 0 || nacks == 0 {
		t.Errorf("no NACK/RTX repair happened (nack %.0f, retransmitted %.0f)", nacks, retrans)
	}
	if keyFrames > 6 {
		t.Errorf("%.0f key frames: loss was repaired with key frames instead of retransmissions", keyFrames)
	}
}
