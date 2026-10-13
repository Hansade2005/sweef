package engine

import (
	"strings"
	"testing"
	"time"
)

func TestWebRTCEventsAndStates(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, negotiateJS, nil)
	evalJSON(t, tab, `(function(){
	  window.ev = {sig: [], gather: [], ice: [], conn: [], nn: 0, endNull: false, dcopen: false};
	  a.onsignalingstatechange = function () { ev.sig.push(a.signalingState); };
	  a.onicegatheringstatechange = function () { ev.gather.push(a.iceGatheringState); };
	  a.oniceconnectionstatechange = function () { ev.ice.push(a.iceConnectionState); };
	  a.onconnectionstatechange = function () { ev.conn.push(a.connectionState); };
	  a.onnegotiationneeded = function () { ev.nn++; };
	  a.addEventListener('icecandidate', function (e) { if (e.candidate === null) ev.endNull = true; });
	  window.d = a.createDataChannel('x'); d.onopen = function () { ev.dcopen = true; };
	  return 1; })()`, nil)
	waitJS(t, tab, `ev.nn === 1`, 5*time.Second) // a data channel on a stable connection needs negotiation
	evalJSON(t, tab, `(negotiate().catch(function (x) { err.push(String(x)); }), 1)`, nil)
	waitJS(t, tab, `ev.dcopen && ev.conn.indexOf('connected') >= 0 && ev.gather.indexOf('complete') >= 0`, 20*time.Second)
	var ev struct {
		Sig, Gather, Ice, Conn []string
		NN                     int
		EndNull                bool
	}
	evalJSON(t, tab, `ev`, &ev)
	if strings.Join(ev.Sig, ",") != "have-local-offer,stable" {
		t.Errorf("signalingstatechange: %v", ev.Sig)
	}
	if len(ev.Gather) < 2 || ev.Gather[0] != "gathering" || ev.Gather[len(ev.Gather)-1] != "complete" {
		t.Errorf("icegatheringstatechange: %v", ev.Gather)
	}
	if len(ev.Ice) == 0 || len(ev.Conn) < 2 || ev.Conn[0] != "connecting" || !ev.EndNull {
		t.Errorf("ice=%v conn=%v endNull=%v", ev.Ice, ev.Conn, ev.EndNull)
	}
	if ev.NN != 1 {
		t.Errorf("negotiationneeded fired %d times, want 1 (createDataChannel on a stable connection)", ev.NN)
	}
	// restartIce: another negotiationneeded and an offer with new credentials
	var res struct{ Same bool }
	evalJSON(t, tab, `(function(){ window.ufrag0 = /a=ice-ufrag:(\S+)/.exec(a.localDescription.sdp)[1]; window.nn0 = ev.nn; a.restartIce(); window.fresh = ''; a.createOffer().then(function (o) { window.fresh = /a=ice-ufrag:(\S+)/.exec(o.sdp)[1]; }); return 1; })()`, nil)
	waitJS(t, tab, `ev.nn === nn0 + 1 && fresh !== ''`, 5*time.Second)
	evalJSON(t, tab, `({same: fresh === ufrag0})`, &res)
	if res.Same {
		t.Error("restartIce() did not produce new ICE credentials")
	}
	// close: states, rejected operations, no events
	var out []string
	evalJSON(t, tab, `(function(){ var o = []; var n = ev.conn.length; a.close(); o.push(a.signalingState, a.connectionState, a.iceConnectionState);
	  try { a.createDataChannel('late'); } catch (e) { o.push(e.name); }
	  a.getStats().catch(function (e) { window.statsErr = e.name; });
	  a.createOffer().catch(function (e) { window.offerErr = e.name; });
	  window.nAfter = n; return o; })()`, &out)
	if strings.Join(out, ",") != "closed,closed,closed,InvalidStateError" {
		t.Fatalf("after close: %v", out)
	}
	waitJS(t, tab, `window.statsErr && window.offerErr`, 5*time.Second)
	tab.Settle(200 * time.Millisecond)
	var extra int
	evalJSON(t, tab, `ev.conn.length - nAfter`, &extra)
	if extra != 0 {
		t.Errorf("close() fired %d connectionstatechange event(s); it must fire none", extra)
	}
}

func TestWebRTCDisabledAndProxy(t *testing.T) {
	srv := rtcServer(t)
	// a proxy is configured: WebRTC (UDP) would bypass it, so constructing a peer throws
	o := DefaultOptions()
	o.SettleMs = 30
	o.ProxyURL = "http://127.0.0.1:9" // never contacted: the page loads from about:blank
	e := New(o)
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Skip("about:blank tab unavailable:", err)
	}
	var name string
	evalJSON(t, tab, `(function(){ try { new RTCPeerConnection(); return 'created'; } catch (e) { return e.name; } })()`, &name)
	if name != "NotSupportedError" {
		t.Fatalf("with a proxy configured the constructor gave %q", name)
	}
	SetWebRTCAllowWithProxy(true)
	t.Cleanup(func() { SetWebRTCAllowWithProxy(false) })
	evalJSON(t, tab, `(function(){ try { new RTCPeerConnection().close(); return 'created'; } catch (e) { return e.name; } })()`, &name)
	if name != "created" {
		t.Fatalf("with the proxy override the constructor gave %q", name)
	}
	// globally disabled: the API does not exist on pages loaded afterwards
	SetWebRTCEnabled(false)
	t.Cleanup(func() { SetWebRTCEnabled(true) })
	e2 := newRTCEngine(t)
	tab2 := newRTCTab(t, e2, srv)
	var typ string
	evalJSON(t, tab2, `typeof RTCPeerConnection + '/' + typeof RTCDataChannel`, &typ)
	if typ != "undefined/undefined" {
		t.Fatalf("WebRTC disabled but typeof = %s", typ)
	}
}
