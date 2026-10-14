package engine

import (
	"testing"
	"time"
)

// A sign-in popup that hands its result over through localStorage or a BroadcastChannel
// reaches the opener of the same origin; another origin hears nothing.
func TestStorageEventAndBroadcastChannelAcrossWindows(t *testing.T) {
	app := newSite(t, nil)
	other := newSite(t, nil)
	app.set("/", `<!doctype html><script>
window.events = []; window.bc = [];
addEventListener('storage', function (e) { window.events.push([e.key, e.oldValue, e.newValue].join('|')); });
var ch = new BroadcastChannel('auth'); ch.onmessage = function (e) { window.bc.push(e.data); };
var peer = new BroadcastChannel('auth'); window.selfGot = 0; peer.onmessage = function () { window.selfGot++; };
</script>`)
	app.set("/pop", `<!doctype html><script>
localStorage.setItem('token', 'abc'); localStorage.setItem('token', 'abc'); localStorage.setItem('token', 'def'); localStorage.removeItem('token');
new BroadcastChannel('auth').postMessage({t: 1, d: new Date(0)});
new BroadcastChannel('other').postMessage('x');
</script>`)
	other.set("/", `<!doctype html><script>window.bc = []; window.ev = []; addEventListener('storage', function(e){window.ev.push(1)}); new BroadcastChannel('auth').onmessage = function (e) { window.bc.push(e.data); };</script>`)
	e := newEng(t)
	tab, _ := e.NewTab(app.URL + "/")
	bystander, _ := e.NewTab(other.URL + "/")
	tab.NoteUserActivation()
	jsonOf(t, tab, `window.open('/pop'); 1`)
	waitFor(t, "popup tab", 3*time.Second, func() bool { return len(e.ListTabs()) == 3 })
	p := e.ListTabs()[2]
	waitURL(t, p, "/pop")
	waitFor(t, "storage events and channel message", 5*time.Second, func() bool {
		tab.Settle(20 * time.Millisecond)
		return jsonOf(t, tab, `window.events.length`) == "3" && jsonOf(t, tab, `window.bc.length`) == "1"
	})
	if got := jsonOf(t, tab, `window.events.join(',')`); got != `"token||abc,token|abc|def,token|def|"` {
		t.Fatalf("storage events: %s", got)
	}
	if got := jsonOf(t, tab, `window.bc[0].t + ':' + (window.bc[0].d instanceof Date)`); got != `"1:true"` {
		t.Fatalf("channel message: %s", got)
	}
	bystander.Settle(50 * time.Millisecond)
	if got := jsonOf(t, bystander, `window.bc.length + ',' + window.ev.length`); got != `"0,0"` {
		t.Fatalf("another origin heard %s", got)
	}
	// channels of one document talk to each other; the sender does not hear itself
	jsonOf(t, tab, `ch.postMessage('local'); 1`)
	waitFor(t, "same-document delivery", 3*time.Second, func() bool {
		tab.Settle(20 * time.Millisecond)
		return jsonOf(t, tab, `window.selfGot`) == "2" // one from the popup, one from the local channel
	})
	if got := jsonOf(t, tab, `window.bc.length`); got != "1" {
		t.Fatalf("the sender heard its own message: %s", got)
	}
}
