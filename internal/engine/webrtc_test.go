package engine

import (
	"encoding/json"
	"fmt"
	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/rtcmedia/testsrc"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// All tests are offline: peers talk over the loopback interface only (the
// rtcTestLoopback switch restricts ICE gathering to it) and use no STUN or TURN.

func rtcServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body><p>webrtc</p></body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func rtcMode(t *testing.T, m WebRTCHostCandidates) {
	t.Helper()
	old := WebRTCHostCandidatePolicy()
	SetWebRTCHostCandidates(m)
	rtcTestLoopback.Store(true)
	t.Setenv("SWEEF_FAKE_DEVICES", "1") // getUserMedia hands out the synthetic camera and microphone
	// The call tests measure picture quality against the rtcmedia test pattern (frame counter + PSNR), so the
	// fake camera's track is replaced by that pattern at the sender; audio keeps the capture tone.
	SetRTCTrackResolver(func(_ *Tab, d RTCTrackDesc) (RTCTrackSource, bool) {
		if d.Kind != "video" || d.Label != "Fake Camera" {
			return RTCTrackSource{}, false
		}
		return RTCTrackSource{OpenVideo: func(c capture.VideoConstraints) (capture.VideoSource, error) {
			w, h, fps := d.Width, d.Height, d.FrameRate
			if w <= 0 {
				w, h = 640, 360
			}
			if fps <= 0 {
				fps = 30
			}
			return testsrc.NewVideo(w, h, fps), nil
		}}, true
	})
	t.Cleanup(func() { SetWebRTCHostCandidates(old); rtcTestLoopback.Store(false); SetRTCTrackResolver(nil) })
}

func newRTCEngine(t *testing.T) *Engine {
	t.Helper()
	o := DefaultOptions()
	o.SettleMs = 30
	e := New(o)
	t.Cleanup(func() { e.Close() })
	return e
}

func newRTCTab(t *testing.T, e *Engine, srv *httptest.Server) *Tab {
	t.Helper()
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	tab.SetMediaBroker(&testBroker{decide: allow}) // permission prompt answered 'allow'
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	return tab
}

// waitJS polls a JavaScript expression until it is truthy.
func waitJS(t *testing.T, tab *Tab, expr string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	var last string
	for time.Now().Before(deadline) {
		raw, err := tab.Evaluate("(" + expr + ")")
		if err == nil {
			last = string(raw)
			if last != "false" && last != "null" && last != "0" && last != `""` && last != "undefined" {
				return
			}
		} else {
			last = err.Error()
		}
		tab.Settle(40 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (last %s)", expr, last)
}

// waitJS2 is waitJS for pages that depend on each other: every tab's event loop is
// pumped while waiting (the app does that for each tab on its own).
func waitJS2(t *testing.T, tabs []*Tab, idx int, expr string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		raw, err := tabs[idx].Evaluate("(" + expr + ")")
		if err == nil && string(raw) != "false" && string(raw) != "null" && string(raw) != "0" {
			return
		}
		for _, tb := range tabs {
			tb.Settle(30 * time.Millisecond)
		}
	}
	t.Fatalf("timed out waiting for %s in tab %d", expr, idx)
}

// negotiateJS wires two connections in the page (a, b) with trickle ICE and a
// full offer/answer, leaving window.a / window.b.
const negotiateJS = `
window.a = new RTCPeerConnection(); window.b = new RTCPeerConnection();
window.err = [];
a.onicecandidate = function (e) { if (e.candidate) b.addIceCandidate(e.candidate).catch(function (x) { err.push('b:' + x); }); };
b.onicecandidate = function (e) { if (e.candidate) a.addIceCandidate(e.candidate).catch(function (x) { err.push('a:' + x); }); };
window.negotiate = async function () {
  var o = await a.createOffer(); await a.setLocalDescription(o);
  await b.setRemoteDescription(a.localDescription);
  var an = await b.createAnswer(); await b.setLocalDescription(an);
  await a.setRemoteDescription(b.localDescription);
};
1`

func TestWebRTCDataChannelLoopback(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, negotiateJS, nil)
	evalJSON(t, tab, `(function(){
	  window.got = []; window.bgot = []; window.opened = false; window.bopened = false;
	  window.dc = a.createDataChannel('chat', {ordered: true});
	  dc.binaryType = 'arraybuffer';
	  dc.onopen = function () { opened = true; dc.send('hello'); dc.send(new Uint8Array([1,2,3,250]).buffer); dc.send(new Blob(['blob!'])); };
	  dc.onmessage = function (m) { got.push(typeof m.data === 'string' ? m.data : Array.from(new Uint8Array(m.data))); };
	  b.ondatachannel = function (e) {
	    var c = e.channel; window.bch = c; c.binaryType = 'arraybuffer';
	    c.onopen = function () { bopened = true; };
	    c.onmessage = function (m) {
	      bgot.push(typeof m.data === 'string' ? m.data : Array.from(new Uint8Array(m.data)));
	      if (bgot.length === 3) { c.send('echo:' + bgot[0]); c.send(new Uint8Array([9,8,7]).buffer); }
	    };
	  };
	  negotiate().catch(function (x) { err.push('neg:' + x); });
	  return 1; })()`, nil)
	waitJS(t, tab, `window.opened && window.bopened && got.length >= 2 && bgot.length >= 3`, 20*time.Second)
	var res struct {
		Got, Bgot []any
		Label     string
		State     string
		Err       []string
		Sig       string
		Conn      string
		Ordered   bool
	}
	evalJSON(t, tab, `({got: got, bgot: bgot, label: bch.label, state: dc.readyState, err: err, sig: a.signalingState, conn: a.connectionState, ordered: bch.ordered})`, &res)
	if len(res.Err) != 0 {
		t.Fatalf("errors: %v", res.Err)
	}
	if fmt.Sprint(res.Bgot) != "[hello [1 2 3 250] [98 108 111 98 33]]" {
		t.Fatalf("b received %v", res.Bgot)
	}
	if fmt.Sprint(res.Got) != "[echo:hello [9 8 7]]" {
		t.Fatalf("a received %v", res.Got)
	}
	if res.Label != "chat" || res.State != "open" || res.Sig != "stable" || !res.Ordered {
		t.Fatalf("unexpected state %+v", res)
	}
	// close both ends: the remote sees the channel close
	evalJSON(t, tab, `(function(){ window.closed = false; bch.onclose = function(){ closed = true; }; dc.close(); return 1; })()`, nil)
	waitJS(t, tab, `window.closed && dc.readyState === 'closed'`, 10*time.Second)
	evalJSON(t, tab, `(a.close(), b.close(), a.connectionState + '/' + a.signalingState)`, nil)
}

func TestWebRTCNegotiatedUnreliableChannels(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, negotiateJS, nil)
	evalJSON(t, tab, `(function(){
	  window.r = [];
	  var x = a.createDataChannel('n', {negotiated: true, id: 7, maxRetransmits: 0, ordered: false});
	  var y = b.createDataChannel('n', {negotiated: true, id: 7, maxRetransmits: 0, ordered: false});
	  window.x = x; window.y = y;
	  y.onmessage = function (m) { r.push(m.data); };
	  x.onopen = function () { x.send('u1'); };
	  negotiate().catch(function (e) { err.push(String(e)); });
	  return 1; })()`, nil)
	waitJS(t, tab, `r.length >= 1`, 20*time.Second)
	var res struct {
		ID, MR    any
		Ord, Neg  bool
		Reliable  bool
		MPLT      any
		Threshold int
		BT        string
	}
	evalJSON(t, tab, `({id: x.id, mr: x.maxRetransmits, ord: x.ordered, neg: x.negotiated, reliable: x.reliable, mplt: x.maxPacketLifeTime, threshold: x.bufferedAmountLowThreshold, bt: x.binaryType})`, &res)
	if fmt.Sprint(res.ID) != "7" || fmt.Sprint(res.MR) != "0" || res.Ord || !res.Neg || res.Reliable || res.MPLT != nil || res.BT != "blob" {
		t.Fatalf("channel attributes %+v", res)
	}
	// argument validation
	var msgs []string
	evalJSON(t, tab, `(function(){ var o = [];
	  function t(f){ try { f(); o.push('ok'); } catch (e) { o.push(e.name || String(e)); } }
	  t(function(){ a.createDataChannel('x', {maxRetransmits: 1, maxPacketLifeTime: 1}); });
	  t(function(){ a.createDataChannel('x', {negotiated: true}); });
	  t(function(){ x.binaryType = 'nope'; });
	  t(function(){ var c = a.createDataChannel('late'); c.send('x'); });
	  return o; })()`, &msgs)
	if strings.Join(msgs, ",") != "TypeError,TypeError,TypeError,InvalidStateError" {
		t.Fatalf("validation: %v", msgs)
	}
}

func TestWebRTCBufferedAmountEvent(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, negotiateJS, nil)
	evalJSON(t, tab, `(function(){
	  window.low = 0; window.peak = 0; window.rx = 0;
	  var d = a.createDataChannel('bulk'); window.d = d;
	  b.ondatachannel = function (e) { e.channel.onmessage = function (m) { rx += m.data.length; }; };
	  d.bufferedAmountLowThreshold = 1000;
	  d.onbufferedamountlow = function () { low++; };
	  d.onopen = function () { var s = 'x'.repeat(16000); for (var i = 0; i < 20; i++) d.send(s); peak = d.bufferedAmount; };
	  negotiate().catch(function (e) { err.push(String(e)); });
	  return 1; })()`, nil)
	waitJS(t, tab, `rx >= 320000 && low >= 1`, 20*time.Second)
	var peak int
	evalJSON(t, tab, `peak`, &peak)
	if peak <= 0 {
		t.Fatalf("bufferedAmount right after sending 320 kB was %d", peak)
	}
}

func TestWebRTCStats(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, negotiateJS, nil)
	evalJSON(t, tab, `(function(){
	  window.ok = false;
	  var d = a.createDataChannel('s'); b.ondatachannel = function(e){ e.channel.onmessage = function(){ ok = true; }; };
	  d.onopen = function () { d.send('stat me'); };
	  negotiate().catch(function (e) { err.push(String(e)); });
	  return 1; })()`, nil)
	waitJS(t, tab, `window.ok && a.connectionState === 'connected'`, 20*time.Second)
	evalJSON(t, tab, `(window.rep = null, a.getStats().then(function(r){ window.rep = r; }), 1)`, nil)
	waitJS(t, tab, `window.rep !== null`, 5*time.Second)
	var res struct {
		Types   map[string]int
		IsMap   bool
		Sent    float64
		Pair    string
		Nom     bool
		Bytes   float64
		Opened  float64
		Channel string
	}
	evalJSON(t, tab, `(function(){ var t = {}, sent = 0, pair = '', nom = false, bytes = 0, opened = 0, ch = '';
	  rep.forEach(function (s) { t[s.type] = (t[s.type] || 0) + 1;
	    if (s.type === 'data-channel') { sent = s.messagesSent; ch = s.label; }
	    if (s.type === 'candidate-pair') { if (s.nominated) nom = true; if (s.bytesSent > bytes) bytes = s.bytesSent; }
	    if (s.type === 'peer-connection') opened = s.dataChannelsOpened;
	  });
	  return {types: t, isMap: rep instanceof Map, sent: sent, pair: pair, nom: nom, bytes: bytes, opened: opened, channel: ch}; })()`, &res)
	for _, k := range []string{"peer-connection", "data-channel", "transport", "candidate-pair", "local-candidate", "remote-candidate", "certificate"} {
		if res.Types[k] == 0 {
			t.Errorf("stats report has no %q entry: %v", k, res.Types)
		}
	}
	if !res.IsMap || res.Sent < 1 || !res.Nom || res.Bytes <= 0 || res.Channel != "s" || res.Opened < 1 {
		t.Fatalf("stats values: %+v", res)
	}
}

func TestWebRTCTrackNegotiation(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, negotiateJS, nil)
	evalJSON(t, tab, `(function(){
	  window.tracks = []; window.streamIds = [];
	  window.local = new MediaStream();
	  var at = __swMakeTrack('audio', 'mic', {}), vt = __swMakeTrack('video', 'cam', {});
	  window.at = at; window.vt = vt;
	  window.sa = a.addTrack(at, local); window.sv = a.addTrack(vt, local);
	  window.tx = b.addTransceiver('audio', {direction: 'recvonly'});
	  b.ontrack = function (e) { tracks.push(e.track.kind + ':' + e.receiver.track.kind + ':' + e.streams.length + ':' + e.transceiver.mid); streamIds.push(e.streams[0] && e.streams[0].id); };
	  window.sdps = {};
	  a.createOffer().then(function (o) { sdps.offer = o.sdp; return negotiate(); }).then(function(){ sdps.done = true; }, function (e) { err.push(String(e)); });
	  return 1; })()`, nil)
	waitJS(t, tab, `window.sdps.done && a.connectionState === 'connected'`, 20*time.Second)
	var res struct {
		Offer     string
		Tracks    []string
		StreamIDs []string
		Senders   int
		SKind     []string
		LocalID   string
		Dir       []string
		Cur       []string
		Err       []string
		BS        int
		Mids      []any
	}
	evalJSON(t, tab, `({offer: sdps.offer, tracks: tracks, streamIds: streamIds, senders: a.getSenders().length, sKind: a.getSenders().map(function(s){return s.track.kind}), localId: local.id, dir: a.getTransceivers().map(function(t){return t.direction}), cur: a.getTransceivers().map(function(t){return t.currentDirection}), err: err, bs: b.getTransceivers().length, mids: a.getTransceivers().map(function(t){return t.mid})})`, &res)
	if len(res.Err) != 0 {
		t.Fatalf("errors %v", res.Err)
	}
	if !strings.Contains(res.Offer, "m=audio") || !strings.Contains(res.Offer, "m=video") {
		t.Fatalf("offer lacks media sections:\n%s", res.Offer)
	}
	if strings.Contains(res.Offer, "m=application") {
		t.Fatalf("offer has a data section without a data channel")
	}
	if !strings.Contains(res.Offer, "a=msid:"+res.LocalID) {
		t.Fatalf("offer does not carry the stream id %s", res.LocalID)
	}
	if res.Senders != 2 || len(res.Tracks) != 2 {
		t.Fatalf("senders=%d tracks=%v", res.Senders, res.Tracks)
	}
	for _, s := range res.StreamIDs {
		if s != res.LocalID {
			t.Fatalf("remote stream id %q, want %q", s, res.LocalID)
		}
	}
	if res.Tracks[0] != "audio:audio:1:0" || res.Tracks[1] != "video:video:1:1" {
		t.Fatalf("track events %v", res.Tracks)
	}
	if strings.Join(res.Cur, ",") != "sendrecv,sendrecv" && strings.Join(res.Cur, ",") != "sendonly,sendonly" {
		t.Fatalf("currentDirection %v", res.Cur)
	}
	// the received track attaches to a media element
	var ok bool
	evalJSON(t, tab, `(function(){ var v = document.createElement('video'); v.srcObject = tracks && b.getReceivers()[0] && new MediaStream([b.getReceivers()[0].track]); return v.srcObject instanceof MediaStream; })()`, &ok)
	if !ok {
		t.Fatal("received track did not attach to a media element")
	}
	// replaceTrack and removeTrack
	var rr []string
	evalJSON(t, tab, `(function(){ window.rr = [];
	  var t2 = __swMakeTrack('audio', 'mic2', {});
	  sa.replaceTrack(t2).then(function(){ rr.push('replaced:' + (sa.track === t2)); return sa.replaceTrack(vt); }).catch(function(e){ rr.push(e.name || String(e)); }).then(function(){ a.removeTrack(sa); rr.push('removed:' + (sa.track === null)); });
	  return 1; })()`, nil)
	waitJS(t, tab, `rr.length === 3`, 5*time.Second)
	evalJSON(t, tab, `rr`, &rr)
	if strings.Join(rr, ",") != "replaced:true,TypeError,removed:true" {
		t.Fatalf("replace/remove: %v", rr)
	}
}

// ICE candidates must not reveal the machine's addresses by default.
func TestWebRTCCandidatePrivacy(t *testing.T) {
	rtcMode(t, WebRTCHostMDNS)
	rtcTestLoopback.Store(false) // gather from the machine's real interfaces
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, `(function(){
	  window.cands = []; window.done = false; window.sdps = [];
	  var p = new RTCPeerConnection();
	  p.createDataChannel('x');
	  p.onicecandidate = function (e) { if (e.candidate) cands.push(e.candidate.candidate); };
	  p.onicegatheringstatechange = function () { if (p.iceGatheringState === 'complete') { done = true; sdps.push(p.localDescription.sdp); p.getStats().then(function (r) { window.stats = []; r.forEach(function (s) { window.stats.push(JSON.stringify(s)); }); }); } };
	  p.createOffer().then(function (o) { return p.setLocalDescription(o); });
	  window.p = p; return 1; })()`, nil)
	waitJS(t, tab, `window.done && window.stats`, 20*time.Second)
	var res struct {
		Cands, Sdps, Stats []string
	}
	evalJSON(t, tab, `({cands: cands, sdps: sdps, stats: stats})`, &res)
	local := localAddrs(t)
	all := strings.Join(res.Cands, "\n") + "\n" + strings.Join(res.Sdps, "\n") + "\n" + strings.Join(res.Stats, "\n")
	for _, ip := range local {
		if strings.Contains(all, ip) {
			t.Fatalf("local address %s leaked to the page:\n%s", ip, all)
		}
	}
	ipRe := regexp.MustCompile(`candidate:\S+ \d \S+ \d+ (\S+) \d+ typ host`)
	for _, c := range res.Cands {
		m := ipRe.FindStringSubmatch(c)
		if m == nil {
			t.Fatalf("unexpected candidate %q", c)
		}
		if !strings.HasSuffix(m[1], ".local") {
			t.Fatalf("host candidate is not obfuscated: %q", c)
		}
	}
	t.Logf("mDNS gathering produced %d host candidate(s) for the page; none carried an address", len(res.Cands))
	for _, line := range strings.Split(strings.Join(res.Sdps, "\n"), "\n") {
		if strings.HasPrefix(line, "c=") && !strings.Contains(line, "0.0.0.0") {
			t.Fatalf("SDP connection line carries an address: %q", line)
		}
	}
}

// gatherHost gathers candidates for a data-channel-only connection and returns the
// candidate lines the page received.
func gatherHost(t *testing.T, tab *Tab, name string) []string {
	t.Helper()
	evalJSON(t, tab, fmt.Sprintf(`(function(){ window.%[1]s = []; window.%[1]sDone = false;
	  var p = new RTCPeerConnection(); p.createDataChannel('x'); window.%[1]sP = p;
	  p.onicecandidate = function (e) { if (e.candidate) %[1]s.push(e.candidate.candidate); else %[1]sDone = true; };
	  p.createOffer().then(function (o) { return p.setLocalDescription(o); });
	  return 1; })()`, name), nil)
	waitJS(t, tab, name+"Done", 15*time.Second)
	var c []string
	evalJSON(t, tab, name, &c)
	return c
}

func TestWebRTCHostCandidateModes(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	raw := gatherHost(t, tab, "raw")
	if len(raw) == 0 || !strings.Contains(strings.Join(raw, "\n"), "127.0.0.1") {
		t.Fatalf("raw mode must publish the loopback address (it is the control for the other modes): %v", raw)
	}
	SetWebRTCHostCandidates(WebRTCHostNone)
	if none := gatherHost(t, tab, "none"); len(none) != 0 {
		t.Fatalf("none mode published %v", none)
	}
	SetWebRTCHostCandidates(WebRTCHostMDNS)
	for _, c := range gatherHost(t, tab, "mdns") {
		if strings.Contains(c, "127.0.0.1") || !strings.Contains(c, ".local ") {
			t.Fatalf("mdns mode published %q", c)
		}
	}
}

func localAddrs(t *testing.T) []string {
	t.Helper()
	var out []string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}

func TestWebRTCCandidatePolicy(t *testing.T) {
	cases := []struct {
		line string
		mode WebRTCHostCandidates
		keep bool
		want string
	}{
		{"candidate:1 1 udp 2130706431 192.168.1.5 5000 typ host", WebRTCHostMDNS, false, ""},
		{"candidate:1 1 udp 2130706431 8b2c0a3e-4b1d-4f0e-9c7d-1234567890ab.local 5000 typ host", WebRTCHostMDNS, true, ""},
		{"candidate:1 1 udp 2130706431 8b2c0a3e-4b1d-4f0e-9c7d-1234567890ab.local 5000 typ host", WebRTCHostNone, false, ""},
		{"candidate:1 1 udp 2130706431 192.168.1.5 5000 typ host", WebRTCHostRaw, true, ""},
		{"candidate:2 1 udp 1694498815 203.0.113.9 6000 typ srflx raddr 192.168.1.5 rport 5000", WebRTCHostMDNS, true, "candidate:2 1 udp 1694498815 203.0.113.9 6000 typ srflx raddr 0.0.0.0 rport 0"},
		{"candidate:3 1 udp 16777215 198.51.100.7 7000 typ relay raddr 203.0.113.9 rport 6000", WebRTCHostNone, true, "candidate:3 1 udp 16777215 198.51.100.7 7000 typ relay raddr 0.0.0.0 rport 0"},
		{"garbage", WebRTCHostMDNS, false, ""},
	}
	for _, c := range cases {
		got, ok := rtcSanitizeCandidate(c.line, c.mode)
		if ok != c.keep {
			t.Errorf("%q mode %d: keep=%v want %v", c.line, c.mode, ok, c.keep)
			continue
		}
		if ok && c.want != "" && got != c.want {
			t.Errorf("%q: got %q want %q", c.line, got, c.want)
		}
	}
	sdp := "v=0\r\na=candidate:1 1 udp 1 10.0.0.2 1 typ host\r\na=candidate:2 1 udp 1 x.local 1 typ host\r\na=end-of-candidates\r\n"
	out := rtcSanitizeSDP(sdp, WebRTCHostMDNS)
	if strings.Contains(out, "10.0.0.2") || !strings.Contains(out, "x.local") || !strings.Contains(out, "end-of-candidates") {
		t.Fatalf("sanitised SDP:\n%s", out)
	}
}

// With no iceServers configured nothing may be sent to a STUN or TURN server: the
// only sockets are host sockets (and mDNS), so a fake STUN server sees nothing.
func TestWebRTCNoStunWithoutIceServers(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	hits := make(chan struct{}, 8)
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := l.ReadFrom(buf); err != nil {
				return
			}
			hits <- struct{}{}
		}
	}()
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	// 1) no servers: nothing reaches the listener
	evalJSON(t, tab, `(function(){ window.g = false; var p = new RTCPeerConnection(); p.createDataChannel('x'); p.onicegatheringstatechange = function(){ if (p.iceGatheringState === 'complete') g = true; }; p.createOffer().then(function(o){ return p.setLocalDescription(o); }); window.p0 = p; return 1; })()`, nil)
	waitJS(t, tab, `window.g`, 15*time.Second)
	select {
	case <-hits:
		t.Fatal("a packet reached the STUN address although the page configured no iceServers")
	case <-time.After(300 * time.Millisecond):
	}
	// 2) with a configured STUN server the binding request is sent to it
	addr := l.LocalAddr().String()
	evalJSON(t, tab, fmt.Sprintf(`(function(){ window.g2 = false; var p = new RTCPeerConnection({iceServers: [{urls: 'stun:%s'}]}); p.createDataChannel('x'); p.onicegatheringstatechange = function(){ if (p.iceGatheringState === 'complete') g2 = true; }; p.createOffer().then(function(o){ return p.setLocalDescription(o); }); window.p1 = p; return 1; })()`, addr), nil)
	for deadline := time.Now().Add(10 * time.Second); ; {
		select {
		case <-hits:
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no STUN binding request reached the configured server")
		}
		tab.Settle(40 * time.Millisecond)
	}
}

func TestWebRTCConfigurationValidation(t *testing.T) {
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	var out []string
	evalJSON(t, tab, `(function(){ var o = [];
	  function t(f){ try { f(); o.push('ok'); } catch (e) { o.push(e.name); } }
	  t(function(){ new RTCPeerConnection({iceServers: [{urls: 'http://x'}]}); });
	  t(function(){ new RTCPeerConnection({iceServers: [{urls: 'turn:t.example'}]}); });
	  t(function(){ new RTCPeerConnection({iceServers: [{}]}); });
	  t(function(){ new RTCPeerConnection({bundlePolicy: 'x'}); });
	  t(function(){ new RTCPeerConnection({iceTransportPolicy: 'relay', bundlePolicy: 'max-bundle', iceServers: [{urls: ['stun:s.example:3478'], }, {urls: 'turn:t.example', username: 'u', credential: 'c'}]}).close(); });
	  t(function(){ var p = new RTCPeerConnection(); p.close(); p.createDataChannel('x'); });
	  t(function(){ var p = new RTCPeerConnection({iceTransportPolicy: 'relay'}); var c = p.getConfiguration(); if (c.iceTransportPolicy !== 'relay' || c.bundlePolicy !== 'balanced') throw new Error('cfg'); p.close(); });
	  return o; })()`, &out)
	want := "SyntaxError,InvalidAccessError,TypeError,TypeError,ok,InvalidStateError,ok"
	if strings.Join(out, ",") != want {
		t.Fatalf("got %v want %s", out, want)
	}
}

func TestWebRTCDescriptionsAndCandidates(t *testing.T) {
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	var res struct {
		Cand     map[string]any
		Types    []string
		JSON     string
		TypeErr  string
		BothNull string
	}
	evalJSON(t, tab, `(function(){ var r = {};
	  var c = new RTCIceCandidate({candidate: 'candidate:842163049 1 udp 1677729535 203.0.113.9 46154 typ srflx raddr 10.0.0.1 rport 5000 generation 0', sdpMid: '0', sdpMLineIndex: 0});
	  r.cand = {f: c.foundation, comp: c.component, proto: c.protocol, prio: c.priority, addr: c.address, port: c.port, type: c.type, ra: c.relatedAddress, rp: c.relatedPort};
	  r.types = ['offer','answer','pranswer','rollback'].map(function (t) { return new RTCSessionDescription({type: t}).type; });
	  r.json = JSON.stringify(new RTCSessionDescription({type: 'offer', sdp: 'v=0'}));
	  try { new RTCSessionDescription({type: 'bogus'}); } catch (e) { r.typeErr = e.name; }
	  try { new RTCIceCandidate({candidate: 'x'}); } catch (e) { r.bothNull = e.name; }
	  return r; })()`, &res)
	if res.Cand["type"] != "srflx" || res.Cand["addr"] != "203.0.113.9" || res.Cand["ra"] != "10.0.0.1" || fmt.Sprint(res.Cand["rp"]) != "5000" || res.Cand["comp"] != "rtp" || res.Cand["proto"] != "udp" {
		t.Fatalf("candidate fields %v", res.Cand)
	}
	if strings.Join(res.Types, ",") != "offer,answer,pranswer,rollback" || res.JSON != `{"type":"offer","sdp":"v=0"}` || res.TypeErr != "TypeError" || res.BothNull != "TypeError" {
		t.Fatalf("%+v", res)
	}
}

func TestWebRTCCertificate(t *testing.T) {
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, `(function(){ window.cr = null; RTCPeerConnection.generateCertificate({name: 'ECDSA', namedCurve: 'P-256'}).then(function (c) { window.cr = c; }, function (e) { window.cr = String(e); }); return 1; })()`, nil)
	waitJS(t, tab, `window.cr !== null`, 10*time.Second)
	var res struct {
		Is     bool
		Alg    string
		FPLen  int
		Future bool
		Offer  string
		Err    string
	}
	evalJSON(t, tab, `(function(){ var fp = cr.getFingerprints(); window.fp0 = fp[0].value; var p = new RTCPeerConnection({certificates: [cr]}); p.createDataChannel('x'); window.po = p; window.offer = ''; p.createOffer().then(function (o) { window.offer = o.sdp; }); return {is: cr instanceof RTCCertificate, alg: fp[0].algorithm, fpLen: fp.length, future: cr.expires > Date.now()}; })()`, &res)
	if !res.Is || res.Alg != "sha-256" || res.FPLen < 1 || !res.Future {
		t.Fatalf("certificate %+v", res)
	}
	waitJS(t, tab, `window.offer.length > 0`, 5*time.Second)
	var fpv, offer string
	evalJSON(t, tab, `fp0`, &fpv)
	evalJSON(t, tab, `offer`, &offer)
	if !strings.Contains(strings.ToLower(offer), "a=fingerprint:sha-256 "+strings.ToLower(fpv)) {
		t.Fatalf("offer does not carry the supplied certificate's fingerprint %s:\n%s", fpv, offer)
	}
}

func TestWebRTCDTMFAndTransports(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, negotiateJS, nil)
	evalJSON(t, tab, `(function(){
	  window.tones = []; window.info = {};
	  var at = __swMakeTrack('audio', 'm', {});
	  window.s = a.addTrack(at, new MediaStream());
	  a.createDataChannel('x');
	  s.dtmf.ontonechange = function (e) { tones.push(e.tone); };
	  negotiate().then(function(){ s.dtmf.insertDTMF('12', 40, 30); }, function (e) { err.push(String(e)); });
	  return 1; })()`, nil)
	waitJS(t, tab, `a.connectionState === 'connected' && a.sctp && a.sctp.state === 'connected' && tones.length >= 3`, 20*time.Second)
	var res struct {
		Tones                         []string
		Dtls, Ice, Role, Sctp, Gather string
		Pair                          bool
		Max                           any
		Same                          bool
		VideoDTMF                     bool
	}
	evalJSON(t, tab, `({tones: tones, dtls: s.transport.state, ice: s.transport.iceTransport.state, role: s.transport.iceTransport.role, sctp: a.sctp.state, gather: s.transport.iceTransport.gatheringState, pair: !!s.transport.iceTransport.getSelectedCandidatePair(), max: a.sctp.maxMessageSize, same: a.sctp.transport === s.transport, videoDTMF: a.addTransceiver('video').sender.dtmf !== null})`, &res)
	if strings.Join(res.Tones, ",") != "1,2," || res.Dtls != "connected" || (res.Ice != "connected" && res.Ice != "completed") || res.Sctp != "connected" || !res.Pair || !res.Same || res.VideoDTMF {
		t.Fatalf("%+v", res)
	}
	if res.Role != "controlling" && res.Role != "controlled" {
		t.Fatalf("role %q", res.Role)
	}
}

func TestWebRTCRollbackAndImplicitDescriptions(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, negotiateJS, nil)
	evalJSON(t, tab, `(function(){ window.out = []; window.done = false;
	  a.createDataChannel('x');
	  (async function () {
	    await a.setLocalDescription();                 // implicit offer
	    out.push(a.signalingState + ':' + a.localDescription.type);
	    await a.setLocalDescription({type: 'rollback'});
	    out.push(a.signalingState + ':' + (a.localDescription === null));
	    await a.setLocalDescription();
	    await b.setRemoteDescription(a.localDescription);
	    await b.setLocalDescription();                 // implicit answer
	    out.push(b.signalingState + ':' + b.localDescription.type);
	    await a.setRemoteDescription(b.localDescription);
	    out.push(a.signalingState);
	    try { await a.setRemoteDescription({type: 'answer', sdp: b.localDescription.sdp}); out.push('no-error'); } catch (e) { out.push(e.name); }
	    done = true;
	  })().catch(function (e) { out.push('FAIL ' + e); done = true; });
	  return 1; })()`, nil)
	waitJS(t, tab, `window.done`, 15*time.Second)
	var out []string
	evalJSON(t, tab, `out`, &out)
	if strings.Join(out, ",") != "have-local-offer:offer,stable:true,stable:answer,stable,InvalidStateError" {
		t.Fatalf("got %v", out)
	}
}

func TestWebRTCDirectionHook(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, `(function(){ window.out = []; window.nn = 0;
	  var p = new RTCPeerConnection(); window.p = p;
	  var tr = p.addTransceiver('audio'); tr.direction = 'recvonly';
	  p.onnegotiationneeded = function () { nn++; };
	  p.createOffer().then(function (o) { out.push(/a=recvonly/.test(o.sdp) ? 'recvonly' : 'other'); tr.direction = 'inactive'; return p.createOffer(); }).then(function (o) { out.push(/a=inactive/.test(o.sdp) ? 'inactive' : 'other'); out.push(tr.direction); });
	  return 1; })()`, nil)
	waitJS(t, tab, `out.length === 3`, 5*time.Second)
	var out []string
	evalJSON(t, tab, `out`, &out)
	if strings.Join(out, ",") != "recvonly,inactive,inactive" {
		t.Fatalf("direction not reflected in the offer: %v (pion changed its internals? see rtcStoreDirection)", out)
	}
}

func TestWebRTCDisabledAndPeersClosedOnNavigation(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	srv := rtcServer(t)
	tab := newRTCTab(t, e, srv)
	base := rtcLive.Load()
	evalJSON(t, tab, negotiateJS, nil)
	if n := rtcLive.Load() - base; n != 2 {
		t.Fatalf("live peers %d, want 2", n)
	}
	if err := tab.Navigate(srv.URL+"/other", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for rtcLive.Load() != base && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := rtcLive.Load() - base; n != 0 {
		t.Fatalf("%d peer connection(s) survived the navigation", n)
	}
	// and a tab closed with live peers
	evalJSON(t, tab, negotiateJS, nil)
	if err := e.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for rtcLive.Load() != base && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := rtcLive.Load() - base; n != 0 {
		t.Fatalf("%d peer connection(s) survived closing the tab", n)
	}
}

// Many connect/close cycles leave no goroutines behind.
func TestWebRTCNoGoroutineLeak(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	srv := rtcServer(t)
	cycle := func(explicit bool) {
		e := New(DefaultOptions())
		tab, err := e.NewTab(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		evalJSON(t, tab, negotiateJS, nil)
		evalJSON(t, tab, `(function(){ window.ok = false;
		  var at = __swMakeTrack('audio', 'm', {}); a.addTrack(at, new MediaStream());
		  var d = a.createDataChannel('x'); d.onopen = function(){ d.send('hi'); };
		  b.ondatachannel = function (e) { e.channel.onmessage = function () { ok = true; }; };
		  negotiate().catch(function (e) { err.push(String(e)); });
		  return 1; })()`, nil)
		waitJS(t, tab, `window.ok`, 20*time.Second)
		if n := rtcLive.Load(); n < 2 {
			t.Fatalf("live peers %d", n)
		}
		// half of the cycles close explicitly, half leave it to the tab teardown
		if explicit {
			evalJSON(t, tab, `(a.close(), b.close(), 1)`, nil)
		}
		_ = e.CloseTab(tab.ID)
		e.Close()
	}
	cycle(true) // warm up package level goroutines
	settle := func() int {
		var n int
		for i := 0; i < 40; i++ {
			runtime.GC()
			n = runtime.NumGoroutine()
			time.Sleep(50 * time.Millisecond)
		}
		return n
	}
	base := settle()
	for i := 0; i < 4; i++ {
		cycle(i%2 == 0)
	}
	after := settle()
	if after > base+2 {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("goroutines grew %d -> %d over 4 connect/close cycles\n%s", base, after, buf)
	}
	t.Logf("goroutines: %d before, %d after 4 cycles", base, after)
	if n := rtcLive.Load(); n != 0 {
		t.Fatalf("%d peers still live", n)
	}
}

// Two tabs of one engine negotiate through the test (the signalling channel is Go).
func TestWebRTCAcrossTwoTabs(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	srv := rtcServer(t)
	t1, t2 := newRTCTab(t, e, srv), newRTCTab(t, e, srv)
	evalJSON(t, t1, `(function(){ window.cands = []; window.gdone = false; window.rx = [];
	  var p = new RTCPeerConnection(); window.p = p;
	  var d = p.createDataChannel('x'); d.onopen = function(){ d.send('from-tab-1'); }; d.onmessage = function (m) { rx.push(m.data); };
	  p.onicecandidate = function (e) { if (!e.candidate) gdone = true; };
	  p.createOffer().then(function (o) { return p.setLocalDescription(o); });
	  return 1; })()`, nil)
	waitJS(t, t1, `window.gdone`, 15*time.Second)
	var offer map[string]string
	evalJSON(t, t1, `p.localDescription.toJSON()`, &offer)
	ob, _ := json.Marshal(offer)
	evalJSON(t, t2, `(function(){ window.rx = []; window.gdone = false;
	  var p = new RTCPeerConnection(); window.p = p;
	  p.ondatachannel = function (e) { e.channel.onmessage = function (m) { rx.push(m.data); e.channel.send('ack:' + m.data); }; };
	  p.onicecandidate = function (e) { if (!e.candidate) gdone = true; };
	  p.setRemoteDescription(`+string(ob)+`).then(function(){ return p.createAnswer(); }).then(function (a) { return p.setLocalDescription(a); });
	  return 1; })()`, nil)
	waitJS(t, t2, `window.gdone`, 15*time.Second)
	var answer map[string]string
	evalJSON(t, t2, `p.localDescription.toJSON()`, &answer)
	ab, _ := json.Marshal(answer)
	evalJSON(t, t1, `(p.setRemoteDescription(`+string(ab)+`), 1)`, nil)
	waitJS2(t, []*Tab{t1, t2}, 0, `rx.length >= 1`, 20*time.Second)
	var rx1, rx2 []string
	evalJSON(t, t1, `rx`, &rx1)
	evalJSON(t, t2, `rx`, &rx2)
	if len(rx1) != 1 || rx1[0] != "ack:from-tab-1" || len(rx2) != 1 || rx2[0] != "from-tab-1" {
		t.Fatalf("tab1=%v tab2=%v", rx1, rx2)
	}
}

// RTC traffic is counted by the browsing context's wire meter (Data Saver).
func TestWebRTCCountedByWireMeter(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	in0, out0 := e.WireTotals()
	evalJSON(t, tab, negotiateJS, nil)
	evalJSON(t, tab, `(function(){ window.rx = 0;
	  var d = a.createDataChannel('x'); d.onopen = function(){ d.send('y'.repeat(30000)); };
	  b.ondatachannel = function (e) { e.channel.onmessage = function (m) { rx += m.data.length; }; };
	  negotiate().catch(function (e) { err.push(String(e)); }); return 1; })()`, nil)
	waitJS(t, tab, `rx >= 30000`, 20*time.Second)
	in1, out1 := e.WireTotals()
	if in1-in0 < 30000 || out1-out0 < 30000 {
		t.Fatalf("wire meter saw in=%d out=%d for a 30000 byte message", in1-in0, out1-out0)
	}
	t.Logf("wire meter: +%d in, +%d out bytes for one 30000 byte message (two peers in one page, so both directions count twice)", in1-in0, out1-out0)
}
