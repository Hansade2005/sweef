package engine

import (
	"strings"
	"testing"
)

// The Async Clipboard API: text and rich items round-trip through the context's clipboard, reading needs the
// clipboard-read permission (asked for on a gesture only), ClipboardItem validates like Chromium's.
func TestAsyncClipboard(t *testing.T) {
	srv := newSWServer(t, basicSW)
	p := newPWAEnv(t, "", nil)
	tab := openTab(t, p.e, srv.URL+"/")
	origin := originOf(srv.URL)

	got := js(t, tab, `
const r = {};
r.api = [typeof navigator.clipboard, typeof Clipboard, typeof ClipboardItem, Object.prototype.toString.call(navigator.clipboard)].join();
r.supports = [ClipboardItem.supports('text/plain'), ClipboardItem.supports('image/png'), ClipboardItem.supports('web text/x-custom'), ClipboardItem.supports('application/pdf')].join();
await navigator.clipboard.writeText('héllo wörld');
r.wrote = true;
r.permRead = (await navigator.permissions.query({ name: 'clipboard-read' })).state;
r.permWrite = (await navigator.permissions.query({ name: 'clipboard-write' })).state;
try { await navigator.clipboard.readText(); r.readNoGesture = 'resolved'; } catch (e) { r.readNoGesture = e.name; }
return JSON.stringify(r);`)
	expect(t, tab, got, `{"api":"object,function,function,[object Clipboard]","supports":"true,true,true,false","wrote":true,"permRead":"prompt","permWrite":"granted","readNoGesture":"NotAllowedError"}`)
	if p.askedN() != 0 {
		t.Fatalf("a read without a user gesture must not ask the user (asked %d times)", p.askedN())
	}
	es, _ := p.ctx.Clipboard().Read()
	if len(es) != 1 || es[0].Type != "text/plain" || string(es[0].Data) != "héllo wörld" {
		t.Fatalf("clipboard after writeText: %+v", es)
	}

	// with a gesture the question is asked once; granted, the read works
	tab.MarkUserActivation()
	got = js(t, tab, `
const r = {};
r.text = await navigator.clipboard.readText();
r.perm = (await navigator.permissions.query({ name: 'clipboard-read' })).state;
r.again = await navigator.clipboard.readText();
return JSON.stringify(r);`)
	expect(t, tab, got, `{"text":"héllo wörld","perm":"granted","again":"héllo wörld"}`)
	if p.askedN() != 1 {
		t.Fatalf("asked %d times", p.askedN())
	}
	if a := p.asked[0]; a.Origin != origin || a.Permission != "clipboard-read" || !a.Gesture {
		t.Fatalf("request = %+v", a)
	}

	// rich items: html and a PNG blob, read back as one ClipboardItem
	got = js(t, tab, `
const r = {};
const png = new Uint8Array([137,80,78,71,13,10,26,10,0,1,2,3]);
await navigator.clipboard.write([new ClipboardItem({
  'text/plain': new Blob(['plain'], { type: 'text/plain' }),
  'text/html': Promise.resolve(new Blob(['<b>bold</b>'], { type: 'text/html' })),
  'image/png': new Blob([png], { type: 'image/png' }) })]);
const items = await navigator.clipboard.read();
r.n = items.length; r.types = items[0].types.join();
r.html = await (await items[0].getType('text/html')).text();
const back = new Uint8Array(await (await items[0].getType('image/png')).arrayBuffer());
r.png = back.length + ':' + back[0] + ',' + back[11];
r.style = items[0].presentationStyle;
try { await items[0].getType('image/gif'); } catch (e) { r.missing = e.name; }
r.text = await navigator.clipboard.readText();
return JSON.stringify(r);`)
	expect(t, tab, got, `{"n":1,"types":"text/plain,text/html,image/png","html":"<b>bold</b>","png":"12:137,3","style":"unspecified","missing":"NotFoundError","text":"plain"}`)

	// validation
	got = js(t, tab, `
const r = {};
try { new ClipboardItem({}); } catch (e) { r.empty = e.name; }
try { new ClipboardItem(); } catch (e) { r.noargs = e.name; }
try { new ClipboardItem({ 'text/plain': 'x' }, { presentationStyle: 'bogus' }); } catch (e) { r.style = e.name; }
try { await navigator.clipboard.write([new ClipboardItem({ 'text/plain': new Blob(['x'], { type: 'text/html' }) })]); } catch (e) { r.mismatch = e.name; }
try { await navigator.clipboard.write([new ClipboardItem({ 'application/pdf': new Blob(['x'], { type: 'application/pdf' }) })]); } catch (e) { r.unsupported = e.name; }
try { await navigator.clipboard.write(['text']); } catch (e) { r.notItem = e.name; }
try { await navigator.clipboard.write([new ClipboardItem({ 'text/plain': 'a' }), new ClipboardItem({ 'text/plain': 'b' })]); } catch (e) { r.multi = e.name; }
await navigator.clipboard.write([new ClipboardItem({ 'text/plain': 'string data' })]);
r.stringData = await navigator.clipboard.readText();
return JSON.stringify(r);`)
	expect(t, tab, got, `{"empty":"TypeError","noargs":"TypeError","style":"TypeError","mismatch":"NotAllowedError","unsupported":"NotAllowedError","notItem":"TypeError","multi":"NotAllowedError","stringData":"string data"}`)

	// a revoked decision is honoured and reported
	p.set(origin, "clipboard-read", "denied")
	p.ctx.PWA().PermissionChanged(origin)
	got = js(t, tab, `
let e1; try { await navigator.clipboard.readText(); } catch (e) { e1 = e.name + ':' + e.message; }
return JSON.stringify({ e1, perm: (await navigator.permissions.query({ name: 'clipboard-read' })).state });`)
	if !strings.Contains(got, "NotAllowedError:Read permission denied.") || !strings.Contains(got, `"perm":"denied"`) {
		t.Fatalf("after revoking: %s", got)
	}
}

// A background tab does not write to the clipboard, and document.hidden follows SetBackground.
func TestClipboardNeedsAVisiblePage(t *testing.T) {
	srv := newSWServer(t, basicSW)
	p := newPWAEnv(t, "", nil)
	tab := openTab(t, p.e, srv.URL+"/")
	got := js(t, tab, `
window.vc = [];
document.addEventListener('visibilitychange', () => vc.push(document.visibilityState));
return JSON.stringify([document.hidden, document.visibilityState]);`)
	expect(t, tab, got, `[false,"visible"]`)
	tab.SetBackground(true)
	got = js(t, tab, `
let w; try { await navigator.clipboard.writeText('x'); w = 'ok'; } catch (e) { w = e.name; }
return JSON.stringify({ hidden: document.hidden, state: document.visibilityState, w, events: vc.join() });`)
	expect(t, tab, got, `{"hidden":true,"state":"hidden","w":"NotAllowedError","events":"hidden"}`)
	tab.SetBackground(false)
	got = js(t, tab, `await navigator.clipboard.writeText('y'); return JSON.stringify({ hidden: document.hidden, events: vc.join() });`)
	expect(t, tab, got, `{"hidden":false,"events":"hidden,visible"}`)
}
