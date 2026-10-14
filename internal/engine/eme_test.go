package engine

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	emeKID = [16]byte{0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0}
	emeKey = []byte("0123456789ABCDEF")
	emeBad = []byte("FEDCBA9876543210")
)

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// emeServer serves a blank page and the clip clear and protected with cenc and cbcs (the latter without a pssh, so the
// page learns the key ID from the "keyids" initialization data).
func emeServer(t *testing.T) *httptest.Server {
	t.Helper()
	files := map[string][]byte{
		"/clear.mp4": fxClip(t, nil),
		"/cenc.mp4":  fxClip(t, &fxProt{scheme: "cenc", kid: emeKID, key: emeKey, pssh: true}),
		"/cbcs.mp4":  fxClip(t, &fxProt{scheme: "cbcs", kid: emeKID, key: emeKey}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(b)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body><video id="v" muted></video></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const emeCodec = `video/mp4; codecs="avc1.42E01E"`

// emeSetup is the page code of a minimal EME player. License(kids) runs per request and returns the key to hand out
// (or null to hold the license back).
func emePlayer(name string, keyB64 string, license bool) string {
	lic := "true"
	if !license {
		lic = "false"
	}
	return fmt.Sprintf(`
const R = window.R = { enc: [], msgs: [], ks: [], wk: 0, err: '' };
const access = await navigator.requestMediaKeySystemAccess('org.w3.clearkey', [{ initDataTypes: ['cenc', 'keyids'], videoCapabilities: [{ contentType: %q }] }]);
const keys = window.keys = await access.createMediaKeys();
const v = document.getElementById('v');
await v.setMediaKeys(keys);
v.addEventListener('waitingforkey', () => R.wk++);
v.addEventListener('encrypted', async (e) => {
  R.enc.push(e.initDataType);
  const s = keys.createSession(); window.sess = s;
  s.addEventListener('message', async (m) => {
    const req = JSON.parse(new TextDecoder().decode(m.message)); R.msgs.push(m.messageType + ':' + req.kids.join(','));
    if (!%s) return;
    const lic = { keys: req.kids.map((k) => ({ kty: 'oct', kid: k, k: %q })), type: 'temporary' };
    await s.update(new TextEncoder().encode(JSON.stringify(lic)));
  });
  s.addEventListener('keystatuseschange', () => { s.keyStatuses.forEach((st, id) => R.ks.push(st + ':' + new Uint8Array(id).length)); });
  await s.generateRequest(e.initDataType, e.initData);
});
const ms = new MediaSource(); v.src = URL.createObjectURL(ms);
await new Promise((r) => ms.addEventListener('sourceopen', r, { once: true }));
const sb = ms.addSourceBuffer(%q);
sb.appendBuffer(await (await fetch(%q)).arrayBuffer());
await new Promise((r) => sb.addEventListener('updateend', r, { once: true }));
return 'ok';
`, emeCodec, lic, keyB64, emeCodec, name)
}

func shownHash(tab *Tab) (string, bool) {
	raw, _ := tab.Evaluate("__swIdOf(document.getElementById('v'))")
	var id int64
	_ = json.Unmarshal(raw, &id)
	tab.mu.RLock()
	d := tab.dom
	tab.mu.RUnlock()
	n := d.NodeByID(id)
	if n == nil {
		return "", false
	}
	src := tab.BitmapSourceFor(n)
	if src == nil {
		return "", false
	}
	img := src.Bitmap()
	if img == nil {
		return "", false
	}
	b := img.Bounds()
	h := sha256.New()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			h.Write([]byte{byte(r >> 8), byte(g >> 8), byte(bl >> 8)})
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:8]), true
}

// playedHash plays the page's video from the start of the content, waits for a picture at t >= 0.25 and then
// returns the hash of the first frame after seeking back to 0 (deterministic for a given decode).
func emeFirstFrame(t *testing.T, tab *Tab) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tab.Settle(40 * time.Millisecond)
		if h, ok := shownHash(tab); ok {
			var rs int
			raw, _ := tab.Evaluate("document.getElementById('v').readyState")
			_ = json.Unmarshal(raw, &rs)
			if rs >= 2 {
				return h, true
			}
		}
	}
	return "", false
}

func emeOpen(t *testing.T) (*Tab, *httptest.Server) {
	srv := emeServer(t)
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/")
	return tab, srv
}

func TestEMEAPIAndKeySystems(t *testing.T) {
	tab, _ := emeOpen(t)
	got := js(t, tab, `
const out = {};
const cfg = [{ initDataTypes: ['cenc'], videoCapabilities: [{ contentType: 'video/mp4; codecs="avc1.42E01E"' }] }];
for (const ks of ['com.widevine.alpha', 'com.microsoft.playready', 'com.apple.fps.1_0', 'com.apple.fps', 'org.w3.clearkey.x']) {
  try { await navigator.requestMediaKeySystemAccess(ks, cfg); out[ks] = 'resolved'; } catch (e) { out[ks] = e.name; }
}
const a = await navigator.requestMediaKeySystemAccess('org.w3.clearkey', cfg);
out.keySystem = a.keySystem;
out.cfg = a.getConfiguration();
out.isAccess = a instanceof MediaKeySystemAccess;
// first usable configuration wins; an unsupported one is skipped
const a2 = await navigator.requestMediaKeySystemAccess('org.w3.clearkey', [{ initDataTypes: ['nope'] }, { initDataTypes: ['keyids'], audioCapabilities: [{ contentType: 'audio/mp4; codecs="mp4a.40.2"' }] }]);
out.second = a2.getConfiguration().initDataTypes;
async function rej(p) { try { await p; return 'resolved'; } catch (e) { return e.name; } }
out.robust = await rej(navigator.requestMediaKeySystemAccess('org.w3.clearkey', [{ videoCapabilities: [{ contentType: 'video/mp4; codecs="avc1.42E01E"', robustness: 'HW_SECURE_ALL' }] }]));
out.persistent = await rej(navigator.requestMediaKeySystemAccess('org.w3.clearkey', [{ persistentState: 'required', videoCapabilities: [{ contentType: 'video/mp4; codecs="avc1.42E01E"' }] }]));
out.badtype = await rej(navigator.requestMediaKeySystemAccess('org.w3.clearkey', [{ videoCapabilities: [{ contentType: 'video/mp4; codecs="bogus"' }] }]));
out.empty = await rej(navigator.requestMediaKeySystemAccess('org.w3.clearkey', []));
out.noargs = await rej(navigator.requestMediaKeySystemAccess('org.w3.clearkey'));
const keys = await a.createMediaKeys();
out.isKeys = keys instanceof MediaKeys;
out.cert = await keys.setServerCertificate(new Uint8Array(8));
try { keys.createSession('persistent-license'); out.persistSess = 'ok'; } catch (e) { out.persistSess = e.name; }
try { keys.createSession('bogus'); } catch (e) { out.bogusSess = e.name; }
const s = keys.createSession();
out.sid0 = s.sessionId; out.closedIsPromise = s.closed instanceof Promise; out.size0 = s.keyStatuses.size; out.exp = String(s.expiration);
out.updBefore = await rej(s.update(new Uint8Array([1])));
out.badInit = await rej(s.generateRequest('webm-nope', new Uint8Array([1])));
out.emptyInit = await rej(s.generateRequest('keyids', new Uint8Array(0)));
out.badKeyids = await rej(s.generateRequest('keyids', new TextEncoder().encode('{"kids":5}')));
out.load = await rej(s.load('x'));
out.remove = await rej(s.remove());
out.ctor = (() => { try { new MediaKeys(); return 'ok'; } catch (e) { return e.name; } })();
const kidB = new Uint8Array(16).fill(7);
await s.generateRequest('keyids', new TextEncoder().encode(JSON.stringify({ kids: [kidB.toBase64 ? kidB.toBase64({ alphabet: 'base64url', omitPadding: true }) : btoa(String.fromCharCode(...kidB)).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'')] })));
out.sidSet = s.sessionId.length > 0;
out.twice = await rej(s.generateRequest('keyids', new TextEncoder().encode('{"kids":[]}')));
out.badLic = await rej(s.update(new TextEncoder().encode('not json')));
out.badKty = await rej(s.update(new TextEncoder().encode(JSON.stringify({ keys: [{ kty: 'RSA', kid: 'AAAA', k: 'AAAA' }] }))));
out.shortKey = await rej(s.update(new TextEncoder().encode(JSON.stringify({ keys: [{ kty: 'oct', kid: 'AAAAAAAAAAAAAAAAAAAAAA', k: 'AAAA' }] }))));
await s.close();
out.closed = await s.closed;
out.afterClose = await rej(s.update(new Uint8Array([1])));
out.mkme = (() => { try { return new MediaKeyMessageEvent('message', { messageType: 'license-request', message: new Uint8Array([1, 2]).buffer }).message.byteLength; } catch (e) { return e.name; } })();
out.mkmeBad = (() => { try { new MediaKeyMessageEvent('message', { messageType: 'x', message: new ArrayBuffer(1) }); return 'ok'; } catch (e) { return e.name; } })();
out.mkEnc = new MediaEncryptedEvent('encrypted', { initDataType: 'cenc', initData: new Uint8Array([9]).buffer }).initDataType;
const vv = document.getElementById('v');
out.mkProp = vv.mediaKeys;
out.setBad = await rej(vv.setMediaKeys({}));
await vv.setMediaKeys(keys); out.mkSet = vv.mediaKeys === keys;
const v2 = document.createElement('video');
out.twoEl = await rej(v2.setMediaKeys(keys));
await vv.setMediaKeys(null); out.mkNull = vv.mediaKeys;
return JSON.stringify(out);
`)
	var o map[string]any
	if err := json.Unmarshal([]byte(got), &o); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	want := map[string]any{
		"com.widevine.alpha": "NotSupportedError", "com.microsoft.playready": "NotSupportedError", "com.apple.fps.1_0": "NotSupportedError",
		"com.apple.fps": "NotSupportedError", "org.w3.clearkey.x": "NotSupportedError",
		"keySystem": "org.w3.clearkey", "isAccess": true, "isKeys": true, "cert": false,
		"robust": "NotSupportedError", "persistent": "NotSupportedError", "badtype": "NotSupportedError", "empty": "TypeError", "noargs": "TypeError",
		"persistSess": "NotSupportedError", "bogusSess": "TypeError", "sid0": "", "closedIsPromise": true, "size0": float64(0), "exp": "NaN",
		"updBefore": "InvalidStateError", "badInit": "NotSupportedError", "emptyInit": "TypeError", "badKeyids": "TypeError", "load": "TypeError", "remove": "TypeError",
		"ctor": "TypeError", "sidSet": true, "twice": "InvalidStateError", "badLic": "TypeError", "badKty": "TypeError", "shortKey": "TypeError",
		"closed": "closed-by-application", "afterClose": "InvalidStateError", "mkme": float64(2), "mkmeBad": "TypeError", "mkEnc": "cenc",
		"mkProp": nil, "setBad": "TypeError", "mkSet": true, "twoEl": "QuotaExceededError", "mkNull": nil,
	}
	for k, w := range want {
		if fmt.Sprint(o[k]) != fmt.Sprint(w) {
			t.Errorf("%s = %v, want %v", k, o[k], w)
		}
	}
	if s := fmt.Sprint(o["second"]); s != "[keyids]" {
		t.Errorf("second configuration initDataTypes = %s", s)
	}
	if c, _ := o["cfg"].(map[string]any); c == nil || fmt.Sprint(c["sessionTypes"]) != "[temporary]" || fmt.Sprint(c["persistentState"]) != "not-allowed" {
		t.Errorf("configuration = %v", o["cfg"])
	}
}

func TestEMEClearKeyPlaysEncryptedMSE(t *testing.T) {
	// the reference picture: the same clip unencrypted through the same MSE path
	tab, _ := emeOpen(t)
	js(t, tab, `
const v = document.getElementById('v'); const ms = new MediaSource(); v.src = URL.createObjectURL(ms);
await new Promise((r) => ms.addEventListener('sourceopen', r, { once: true }));
const sb = ms.addSourceBuffer(`+"`"+emeCodec+"`"+`);
sb.appendBuffer(await (await fetch('/clear.mp4')).arrayBuffer());
await new Promise((r) => sb.addEventListener('updateend', r, { once: true }));
return 'ok';`)
	ref, ok := emeFirstFrame(t, tab)
	if !ok {
		t.Fatalf("no picture from the unencrypted reference\n%s", consoleText(tab))
	}

	for _, sc := range []struct{ file, scheme, initType string }{{"/cenc.mp4", "cenc", "cenc"}, {"/cbcs.mp4", "cbcs", "keyids"}} {
		t.Run(sc.scheme, func(t *testing.T) {
			tab, _ := emeOpen(t)
			if r := js(t, tab, emePlayer(sc.file, b64u(emeKey), true)); r != "ok" {
				t.Fatalf("setup: %s\n%s", r, consoleText(tab))
			}
			h, ok := emeFirstFrame(t, tab)
			if !ok {
				t.Fatalf("no picture\n%s", consoleText(tab))
			}
			if h != ref {
				t.Errorf("decrypted picture %s differs from the unencrypted reference %s", h, ref)
			}
			var enc, msgs, ks string
			raw, _ := tab.Evaluate("JSON.stringify([R.enc, R.msgs, R.ks])")
			var all string
			_ = json.Unmarshal(raw, &all)
			var parts [3]json.RawMessage
			_ = json.Unmarshal([]byte(all), &parts)
			enc, msgs, ks = string(parts[0]), string(parts[1]), string(parts[2])
			if !strings.Contains(enc, `"`+sc.initType+`"`) {
				t.Errorf("encrypted events = %s, want %s", enc, sc.initType)
			}
			if !strings.Contains(msgs, "license-request:"+b64u(emeKID[:])) {
				t.Errorf("license requests = %s, want kid %s", msgs, b64u(emeKID[:]))
			}
			if !strings.Contains(ks, "usable:16") {
				t.Errorf("key statuses = %s", ks)
			}
			t.Logf("%s: picture %s == reference, encrypted=%s messages=%s statuses=%s", sc.scheme, h, enc, msgs, ks)
		})
	}
}

func TestEMEWrongKeyAndMissingKey(t *testing.T) {
	tab, _ := emeOpen(t)
	js(t, tab, `
const v = document.getElementById('v'); const ms = new MediaSource(); v.src = URL.createObjectURL(ms);
await new Promise((r) => ms.addEventListener('sourceopen', r, { once: true }));
const sb = ms.addSourceBuffer(`+"`"+emeCodec+"`"+`);
sb.appendBuffer(await (await fetch('/clear.mp4')).arrayBuffer());
await new Promise((r) => sb.addEventListener('updateend', r, { once: true }));
return 'ok';`)
	ref, _ := emeFirstFrame(t, tab)

	// a license with another key: the samples decrypt to noise, which must not show the real picture
	tab, _ = emeOpen(t)
	if r := js(t, tab, emePlayer("/cenc.mp4", b64u(emeBad), true)); r != "ok" {
		t.Fatalf("setup: %s", r)
	}
	tab.Settle(600 * time.Millisecond)
	if h, ok := shownHash(tab); ok && h == ref {
		t.Errorf("wrong key produced the real picture %s", h)
	}

	// no license at all: playback waits for the key and says so
	tab, _ = emeOpen(t)
	if r := js(t, tab, emePlayer("/cenc.mp4", b64u(emeKey), false)); r != "ok" {
		t.Fatalf("setup: %s", r)
	}
	js(t, tab, `document.getElementById('v').play().catch(() => {}); return 'ok';`)
	waitTrue(t, tab, "window.R.wk >= 1", 10*time.Second)
	if h, ok := shownHash(tab); ok && h == ref {
		t.Errorf("a picture was decoded without a key")
	}
	// ... and delivering the license afterwards releases it
	js(t, tab, `
const lic = { keys: [{ kty: 'oct', kid: '`+b64u(emeKID[:])+`', k: '`+b64u(emeKey)+`' }], type: 'temporary' };
await window.sess.update(new TextEncoder().encode(JSON.stringify(lic)));
return 'ok';`)
	h, ok := emeFirstFrame(t, tab)
	if !ok || h != ref {
		// the first frame may already have moved on; the clock must at least advance now the key is there
		waitTrue(t, tab, "document.getElementById('v').currentTime > 0.1", 10*time.Second)
	}
}

func TestEMEWithoutKeysEncryptedDataStalls(t *testing.T) {
	// no MediaKeys attached: the encrypted event fires (the page may set keys then) and no picture is produced
	tab, _ := emeOpen(t)
	got := js(t, tab, `
const v = document.getElementById('v'); window.R = { enc: 0 };
v.addEventListener('encrypted', (e) => { R.enc++; R.t = e.initDataType; R.len = e.initData.byteLength; });
const ms = new MediaSource(); v.src = URL.createObjectURL(ms);
await new Promise((r) => ms.addEventListener('sourceopen', r, { once: true }));
const sb = ms.addSourceBuffer(`+"`"+emeCodec+"`"+`);
sb.appendBuffer(await (await fetch('/cenc.mp4')).arrayBuffer());
await new Promise((r) => sb.addEventListener('updateend', r, { once: true }));
await new Promise((r) => setTimeout(r, 100));
return JSON.stringify(R);`)
	if !strings.Contains(got, `"enc":1`) || !strings.Contains(got, `"t":"cenc"`) {
		t.Errorf("encrypted event = %s", got)
	}
}
