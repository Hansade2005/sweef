package engine

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/pushd"
	"github.com/sweefbrowser/sweef/internal/webpush"
)

// ---- an application server using only the standard library (written apart from internal/webpush) -------------------

func testHKDF(salt, ikm, info []byte, n int) []byte {
	ext := hmac.New(sha256.New, salt)
	ext.Write(ikm)
	prk := ext.Sum(nil)
	var out, prev []byte
	for i := byte(1); len(out) < n; i++ {
		m := hmac.New(sha256.New, prk)
		m.Write(prev)
		m.Write(info)
		m.Write([]byte{i})
		prev = m.Sum(nil)
		out = append(out, prev...)
	}
	return out[:n]
}

func appEncrypt(t testing.TB, p256dh, auth, msg []byte) []byte {
	t.Helper()
	asKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uaKey, err := ecdh.P256().NewPublicKey(p256dh)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := asKey.ECDH(uaKey)
	if err != nil {
		t.Fatal(err)
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	asPub := asKey.PublicKey().Bytes()
	ikm := testHKDF(auth, secret, append(append([]byte("WebPush: info\x00"), p256dh...), asPub...), 32)
	blk, _ := aes.NewCipher(testHKDF(salt, ikm, []byte("Content-Encoding: aes128gcm\x00"), 16))
	gcm, _ := cipher.NewGCM(blk)
	ct := gcm.Seal(nil, testHKDF(salt, ikm, []byte("Content-Encoding: nonce\x00"), 12), append(append([]byte{}, msg...), 2), nil)
	hdr := append(append([]byte{}, salt...), 0, 0, 0, 0, byte(len(asPub)))
	binary.BigEndian.PutUint32(hdr[16:20], 4096)
	return append(append(hdr, asPub...), ct...)
}

// pushedTo posts msg to an endpoint like an application server does and returns the status.
func pushedTo(t testing.TB, endpoint string, p256dh, auth, msg []byte, vapid *webpush.VAPIDKey, extra map[string]string) int {
	t.Helper()
	var body []byte
	if msg != nil {
		body = appEncrypt(t, p256dh, auth, msg)
	}
	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	req.Header.Set("TTL", "300")
	if body != nil {
		req.Header.Set("Content-Encoding", "aes128gcm")
	}
	if vapid != nil {
		u := req.URL
		a, err := vapid.Authorization(u.Scheme+"://"+u.Host, "mailto:ops@example.com", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", a)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

type subInfo struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s subInfo) keys(t testing.TB) (p256dh, auth []byte) {
	t.Helper()
	p, err1 := webpush.DecodeB64(s.Keys.P256dh)
	a, err2 := webpush.DecodeB64(s.Keys.Auth)
	if err1 != nil || err2 != nil {
		t.Fatalf("bad keys %+v", s)
	}
	return p, a
}

func newRelay(t *testing.T, cfg pushd.Config) (*pushd.Server, *httptest.Server) {
	t.Helper()
	if cfg.IPRate == 0 {
		cfg.IPRate, cfg.IPBurst, cfg.EndpointRate, cfg.EndpointBurst = 1000, 1000, 1000, 1000
	}
	s, err := pushd.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })
	return s, srv
}

// pushWorkerJS shows a notification for every push that carries JSON {title, body}; "silent" does nothing.
const pushWorkerJS = `
self.addEventListener('activate', (e) => e.waitUntil(clients.claim()));
const tell = async (m) => { for (const c of await clients.matchAll({ includeUncontrolled: true })) c.postMessage(m); };
self.addEventListener('push', (e) => {
  if (!e.data) { e.waitUntil(tell({ kind: 'empty' })); return; }
  e.waitUntil((async () => {
    const text = await e.data.text();
    let d = null; try { d = JSON.parse(text); } catch (x) { /* plain text */ }
    const buf = await e.data.arrayBuffer(); const blob = await e.data.blob();
    await tell({ kind: 'push', text, bytes: buf.byteLength, blob: blob.size, json: d && d.title });
    if (d && d.title === 'silent') return;
    if (d && d.title) await self.registration.showNotification(d.title, { body: d.body, data: d });
  })());
});
self.addEventListener('pushsubscriptionchange', (e) => {
  e.waitUntil(tell({ kind: 'change', hadOld: !!e.oldSubscription, oldEndpoint: e.oldSubscription && e.oldSubscription.endpoint, newEndpoint: e.newSubscription && e.newSubscription.endpoint }));
});
`

func subscribeScript(key string) string {
	return swHelpers + `
const reg = await regReady('/sw.js');
window.__msgs = []; navigator.serviceWorker.addEventListener('message', (e) => __msgs.push(e.data));
const sub = await reg.pushManager.subscribe({ userVisibleOnly: true` + key + ` });
return JSON.stringify(sub.toJSON());`
}

func TestPushSubscribeErrorsWithoutAService(t *testing.T) {
	t.Setenv("SWEEF_PUSH_SERVICE", "")
	srv := newSWServer(t, pushWorkerJS)
	p := newPWAEnv(t, "", nil)
	p.set(originOf(srv.URL), "notifications", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, swHelpers+`window.__reg = await regReady('/sw.js'); return 1;`)
	vk, _ := webpush.NewVAPIDKey()
	got := js(t, tab, `
const r = {}, pm = __reg.pushManager;
const attempt = async (label, opts) => { try { await pm.subscribe(opts); r[label] = 'ok'; } catch (e) { r[label] = e.name + ': ' + e.message; } };
await attempt('noservice', { userVisibleOnly: true });
await attempt('notvisible', { userVisibleOnly: false });
await attempt('nooptions', undefined);
await attempt('badkey', { userVisibleOnly: true, applicationServerKey: new Uint8Array(10) });
await attempt('badstr', { userVisibleOnly: true, applicationServerKey: '%%%' });
r.sub = await pm.getSubscription();
r.state = await pm.permissionState({ userVisibleOnly: true });
r.encodings = PushManager.supportedContentEncodings.join(',');
r.hasPush = typeof PushManager + typeof PushSubscription + typeof PushEvent + typeof PushMessageData;
return JSON.stringify(r);`)
	var r map[string]any
	if err := json.Unmarshal([]byte(got), &r); err != nil {
		t.Fatal(got)
	}
	if r["noservice"] != "AbortError: Registration failed - push service not available" {
		t.Fatalf("no service: %v", r["noservice"])
	}
	if !strings.HasPrefix(r["notvisible"].(string), "NotSupportedError") || !strings.HasPrefix(r["badkey"].(string), "InvalidAccessError") || !strings.HasPrefix(r["badstr"].(string), "InvalidAccessError") {
		t.Fatalf("validation: %v", r)
	}
	if !strings.HasPrefix(r["nooptions"].(string), "NotSupportedError") {
		t.Fatalf("nooptions: %v", r["nooptions"])
	}
	if r["sub"] != nil || r["state"] != "granted" || r["encodings"] != "aes128gcm" || r["hasPush"] != "functionfunctionfunctionfunction" {
		t.Fatalf("state: %v", r)
	}
	if p.ctx.PWA().PushServiceConfigured() {
		t.Fatal("no service is configured")
	}
	_ = vk
}

func TestPushEndToEndThroughTheRelay(t *testing.T) {
	relay, rs := newRelay(t, pushd.Config{})
	srv := newSWServer(t, pushWorkerJS)
	p := newPWAEnv(t, t.TempDir(), func(o *PWAOptions) { o.PushService = rs.URL })
	origin := originOf(srv.URL)
	p.set(origin, "notifications", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	vk, _ := webpush.NewVAPIDKey()
	got := js(t, tab, subscribeScript(`, applicationServerKey: '`+webpush.B64(vk.Public())+`'`))
	var sub subInfo
	if err := json.Unmarshal([]byte(got), &sub); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sub.Endpoint, rs.URL+"/wpush/v2/") || !relay.EndpointValid(sub.Endpoint) {
		t.Fatalf("endpoint = %q", sub.Endpoint)
	}
	p256dh, auth := sub.keys(t)
	if len(p256dh) != 65 || p256dh[0] != 4 || len(auth) != 16 {
		t.Fatalf("keys: %d %d", len(p256dh), len(auth))
	}
	got = js(t, tab, `
const reg = await navigator.serviceWorker.ready;
const s = await reg.pushManager.getSubscription();
const s2 = await reg.pushManager.getSubscription();
return JSON.stringify({ same: s.endpoint === s2.endpoint, exp: s.expirationTime, uvo: s.options.userVisibleOnly, key: Array.from(new Uint8Array(s.options.applicationServerKey)).length,
  p256: s.getKey('p256dh').byteLength, auth: s.getKey('auth').byteLength, other: s.getKey('nope'), json: Object.keys(s.toJSON()).sort().join(','), state: await reg.pushManager.permissionState({ userVisibleOnly: true }),
  again: (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: `+"'"+webpush.B64(vk.Public())+"'"+` })).endpoint === s.endpoint });`)
	expect(t, tab, got, `{"same":true,"exp":null,"uvo":true,"key":65,"p256":65,"auth":16,"other":null,"json":"endpoint,expirationTime,keys","state":"granted","again":true}`)
	other, _ := webpush.NewVAPIDKey()
	if got := js(t, tab, `const reg = await navigator.serviceWorker.ready; try { await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: '`+webpush.B64(other.Public())+`' }); return 'ok'; } catch (e) { return e.name; }`); got != "InvalidStateError" {
		t.Fatalf("subscribing with another key must fail: %s", got)
	}

	// the application server posts; the worker's push handler shows a notification
	msg := `{"title":"Score","body":"3-2 at half time – café"}`
	if st := pushedTo(t, sub.Endpoint, p256dh, auth, []byte(msg), vk, nil); st != 201 {
		t.Fatalf("POST = %d", st)
	}
	shown := p.waitShown(1)
	if shown[0].Title != "Score" || shown[0].Body != "3-2 at half time – café" || shown[0].Origin != origin {
		t.Fatalf("notification = %+v", shown[0])
	}
	waitUntil(t, "the worker's report", func() bool {
		return strings.Contains(js(t, tab, `return JSON.stringify(__msgs.filter((m) => m.kind === 'push').map((m) => [m.json, m.bytes, m.blob]));`), `["Score",`)
	})
	// without the right VAPID key the relay refuses before the browser is involved
	if st := pushedTo(t, sub.Endpoint, p256dh, auth, []byte(msg), other, nil); st != 403 {
		t.Fatalf("wrong VAPID key = %d", st)
	}
	if st := pushedTo(t, sub.Endpoint, p256dh, auth, []byte(msg), nil, nil); st != 401 {
		t.Fatalf("missing VAPID = %d", st)
	}

	// an empty push has no data; a plain text push can be read as text and bytes
	js(t, tab, `__msgs.length = 0; return 1;`)
	pushedTo(t, sub.Endpoint, nil, nil, nil, vk, nil)
	waitUntil(t, "empty push", func() bool { return strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `"empty"`) })
	pushedTo(t, sub.Endpoint, p256dh, auth, []byte("héllo"), vk, nil)
	waitUntil(t, "text push", func() bool {
		return strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `"text":"héllo","bytes":6,"blob":6`)
	})

	// unsubscribe: the endpoint answers 410 afterwards and there is no subscription
	got = js(t, tab, `const reg = await navigator.serviceWorker.ready; const s = await reg.pushManager.getSubscription(); const a = await s.unsubscribe(); const b = await reg.pushManager.getSubscription(); const c = await s.unsubscribe(); return JSON.stringify([a, b, c]);`)
	expect(t, tab, got, `[true,null,false]`)
	waitUntil(t, "410 after unsubscribe", func() bool { return pushedTo(t, sub.Endpoint, p256dh, auth, []byte("x"), vk, nil) == 410 })
}

func TestPushSilentMessageGetsAGenericNotification(t *testing.T) {
	_, rs := newRelay(t, pushd.Config{})
	srv := newSWServer(t, pushWorkerJS)
	p := newPWAEnv(t, "", func(o *PWAOptions) { o.PushService = rs.URL })
	origin := originOf(srv.URL)
	p.set(origin, "notifications", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	var sub subInfo
	json.Unmarshal([]byte(js(t, tab, subscribeScript(""))), &sub)
	p256dh, auth := sub.keys(t)
	pushedTo(t, sub.Endpoint, p256dh, auth, []byte(`{"title":"silent"}`), nil, nil)
	// the grace period runs on the context's clock, which the test controls
	waitUntil(t, "the grace timer", func() bool { return p.clock.Pending() > 0 })
	if len(p.rec.Shown()) != 0 {
		t.Fatal("the generic notification must wait for the grace period")
	}
	p.clock.Advance(time.Second)
	shown := p.waitShown(1)
	if shown[0].Tag != "user_visible_auto_notification" || shown[0].Body != "This site has been updated in the background." || shown[0].Title != strings.TrimPrefix(origin, "http://") {
		t.Fatalf("generic notification = %+v", shown[0])
	}
	// a push that does show a notification gets no generic one
	pushedTo(t, sub.Endpoint, p256dh, auth, []byte(`{"title":"real"}`), nil, nil)
	shown = p.waitShown(2)
	waitUntil(t, "the grace timer", func() bool { return p.clock.Pending() > 0 })
	p.clock.Advance(time.Second)
	time.Sleep(200 * time.Millisecond)
	if n := len(p.rec.Shown()); n != 2 || shown[1].Title != "real" {
		t.Fatalf("shown %d: %+v", n, p.rec.Shown())
	}
	// clicking the generic notification takes the user to the site
	p.rec.Click(shown[0].ID)
	waitUntil(t, "focus the site", func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.focused) > 0 })
}

func TestPushWhileTheBrowserWasClosedIsDeliveredAfterRestart(t *testing.T) {
	relay, rs := newRelay(t, pushd.Config{})
	srv := newSWServer(t, pushWorkerJS)
	dir := t.TempDir()
	p1 := newPWAEnv(t, dir, func(o *PWAOptions) { o.PushService = rs.URL })
	origin := originOf(srv.URL)
	p1.set(origin, "notifications", "granted")
	tab := openTab(t, p1.e, srv.URL+"/")
	var sub subInfo
	json.Unmarshal([]byte(js(t, tab, subscribeScript(""))), &sub)
	p256dh, auth := sub.keys(t)
	p1.e.Close() // the browser quits
	waitUntil(t, "relay sees the browser gone", func() bool { return relay.Stats().Connected == 0 })
	if st := pushedTo(t, sub.Endpoint, p256dh, auth, []byte(`{"title":"while you were away","body":"b"}`), nil, nil); st != 201 {
		t.Fatalf("POST = %d", st)
	}
	if relay.Stats().Stored != 1 {
		t.Fatalf("the relay must keep the message: %+v", relay.Stats())
	}

	p2 := newPWAEnv(t, dir, func(o *PWAOptions) { o.PushService = rs.URL })
	p2.set(origin, "notifications", "granted")
	shown := p2.waitShown(1)
	if shown[0].Title != "while you were away" {
		t.Fatalf("shown after restart: %+v", shown)
	}
	waitUntil(t, "acknowledged", func() bool { return relay.Stats().Stored == 0 })
}

func TestPushPermissionAndRevocation(t *testing.T) {
	relay, rs := newRelay(t, pushd.Config{})
	srv := newSWServer(t, pushWorkerJS)
	p := newPWAEnv(t, "", func(o *PWAOptions) { o.PushService = rs.URL })
	origin := originOf(srv.URL)
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, swHelpers+`window.__reg = await regReady('/sw.js'); return 1;`)
	try := `try { await __reg.pushManager.subscribe({ userVisibleOnly: true }); return 'ok'; } catch (e) { return e.name + ': ' + e.message; }`

	// no gesture: the question is not asked and nothing is subscribed
	if got := js(t, tab, try); got != "NotAllowedError: Registration failed - permission denied" {
		t.Fatalf("no gesture: %s", got)
	}
	if p.askedN() != 0 {
		t.Fatal("asked without a gesture")
	}
	// the user says no
	p.answer = "denied"
	tab.MarkUserActivation()
	if got := js(t, tab, try); got != "NotAllowedError: Registration failed - permission denied" {
		t.Fatalf("denied: %s", got)
	}
	if p.askedN() != 1 {
		t.Fatalf("asked %d times", p.askedN())
	}
	// blocked sites are not asked again
	if got := js(t, tab, try); got != "NotAllowedError: Registration failed - permission denied" || p.askedN() != 1 {
		t.Fatalf("blocked: %s asked=%d", got, p.askedN())
	}
	// allowed later
	p.set(origin, "notifications", "granted")
	var sub subInfo
	json.Unmarshal([]byte(js(t, tab, `const s = await __reg.pushManager.subscribe({ userVisibleOnly: true }); return JSON.stringify(s.toJSON());`)), &sub)
	p256dh, auth := sub.keys(t)
	if st := pushedTo(t, sub.Endpoint, p256dh, auth, []byte(`{"title":"ok"}`), nil, nil); st != 201 {
		t.Fatal(st)
	}
	p.waitShown(1)
	// revoking the permission ends the subscription at the service
	p.set(origin, "notifications", "denied")
	p.ctx.PWA().PermissionChanged(origin)
	waitUntil(t, "endpoint gone after revoking", func() bool { return pushedTo(t, sub.Endpoint, p256dh, auth, []byte("x"), nil, nil) == 410 })
	if got := js(t, tab, `return String(await __reg.pushManager.getSubscription());`); got != "null" {
		t.Fatalf("subscription after revoking: %s", got)
	}
	if relay.Stats().Channels != 0 {
		t.Fatalf("relay still has %d channels", relay.Stats().Channels)
	}
}

func TestPushEndpointDiesWithTheRegistration(t *testing.T) {
	_, rs := newRelay(t, pushd.Config{})
	srv := newSWServer(t, pushWorkerJS)
	p := newPWAEnv(t, "", func(o *PWAOptions) { o.PushService = rs.URL })
	p.set(originOf(srv.URL), "notifications", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	var sub subInfo
	json.Unmarshal([]byte(js(t, tab, subscribeScript(""))), &sub)
	p256dh, auth := sub.keys(t)
	js(t, tab, `const reg = await navigator.serviceWorker.ready; await reg.unregister(); return 1;`)
	waitUntil(t, "410 after unregister", func() bool { return pushedTo(t, sub.Endpoint, p256dh, auth, []byte("x"), nil, nil) == 410 })
}

func TestPushServiceFromEnvironmentAndOptionsPrecedence(t *testing.T) {
	_, rs := newRelay(t, pushd.Config{})
	srv := newSWServer(t, pushWorkerJS)
	t.Setenv("SWEEF_PUSH_SERVICE", rs.URL)
	p := newPWAEnv(t, "", nil)
	p.set(originOf(srv.URL), "notifications", "granted")
	if !p.ctx.PWA().PushServiceConfigured() {
		t.Fatal("SWEEF_PUSH_SERVICE is not used")
	}
	tab := openTab(t, p.e, srv.URL+"/")
	var sub subInfo
	if err := json.Unmarshal([]byte(js(t, tab, subscribeScript(""))), &sub); err != nil || !strings.HasPrefix(sub.Endpoint, rs.URL) {
		t.Fatalf("env service: %+v %v", sub, err)
	}
	// an explicit option wins over the environment
	_, rs2 := newRelay(t, pushd.Config{})
	p2 := newPWAEnv(t, "", func(o *PWAOptions) { o.PushService = rs2.URL })
	p2.set(originOf(srv.URL), "notifications", "granted")
	tab2 := openTab(t, p2.e, srv.URL+"/")
	sub = subInfo{}
	json.Unmarshal([]byte(js(t, tab2, subscribeScript(""))), &sub)
	if !strings.HasPrefix(sub.Endpoint, rs2.URL) {
		t.Fatalf("option must win: %s", sub.Endpoint)
	}
}

// fakeTransport is a push service that lives in the test: it shows another transport can be plugged in, and it lets
// the test play a service that forgets the browser.
type fakeTransport struct {
	mu    sync.Mutex
	h     webpush.Handler
	chans map[string][]byte
	acks  []string
	n     int
}

func (f *fakeTransport) Register(ctx context.Context, chid string, key []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.chans[chid] = key
	return "https://push.fake.invalid/e/" + chid, nil
}
func (f *fakeTransport) Unregister(ctx context.Context, chid string) error {
	f.mu.Lock()
	delete(f.chans, chid)
	f.mu.Unlock()
	return nil
}
func (f *fakeTransport) Ack(chid, id string, code webpush.AckCode) {
	f.mu.Lock()
	f.acks = append(f.acks, chid[:4]+":"+id+":"+itoa(int(code)))
	f.mu.Unlock()
}
func (f *fakeTransport) Close() error { return nil }
func (f *fakeTransport) ackLog() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.acks, ",")
}

func TestPushWithAnotherTransportAcksAndResubscribes(t *testing.T) {
	ft := &fakeTransport{chans: map[string][]byte{}}
	srv := newSWServer(t, pushWorkerJS)
	p := newPWAEnv(t, t.TempDir(), func(o *PWAOptions) {
		o.PushTransport = func(h webpush.Handler, uaid string) (webpush.Transport, error) {
			ft.h = h
			h.OnIdentity("uaid-1")
			return ft, nil
		}
	})
	p.set(originOf(srv.URL), "notifications", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	var sub subInfo
	json.Unmarshal([]byte(js(t, tab, subscribeScript(""))), &sub)
	if !strings.HasPrefix(sub.Endpoint, "https://push.fake.invalid/e/") {
		t.Fatalf("endpoint %q", sub.Endpoint)
	}
	chid := strings.TrimPrefix(sub.Endpoint, "https://push.fake.invalid/e/")
	p256dh, auth := sub.keys(t)

	// a good message, a message that does not decrypt, a message for a channel nobody knows
	ft.h.OnMessage(webpush.Message{ChannelID: chid, ID: "m1", Data: appEncrypt(t, p256dh, auth, []byte(`{"title":"fine"}`))})
	p.waitShown(1)
	bad := appEncrypt(t, p256dh, auth, []byte("x"))
	bad[len(bad)-1] ^= 1
	ft.h.OnMessage(webpush.Message{ChannelID: chid, ID: "m2", Data: bad})
	ft.h.OnMessage(webpush.Message{ChannelID: "no-such-channel", ID: "m3"})
	waitUntil(t, "acks", func() bool { return strings.Count(ft.ackLog(), ",") == 2 })
	if got := ft.ackLog(); got != chid[:4]+":m1:100,"+chid[:4]+":m2:101,no-s:m3:102" {
		t.Fatalf("acks = %s", got)
	}
	if len(p.rec.Shown()) != 1 {
		t.Fatalf("a message that does not decrypt must not reach the worker: %d shown", len(p.rec.Shown()))
	}

	// the service forgets the browser: the subscription is replaced and the worker is told
	js(t, tab, `__msgs.length = 0; return 1;`)
	ft.h.OnReset("uaid-2")
	waitUntil(t, "pushsubscriptionchange", func() bool { return strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `"kind":"change"`) })
	msgs := js(t, tab, `return JSON.stringify(__msgs.filter((m) => m.kind === 'change'));`)
	var ch []struct {
		HadOld      bool
		OldEndpoint string
		NewEndpoint string
	}
	if err := json.Unmarshal([]byte(msgs), &ch); err != nil || len(ch) != 1 || !ch[0].HadOld || ch[0].OldEndpoint != sub.Endpoint || ch[0].NewEndpoint == "" || ch[0].NewEndpoint == sub.Endpoint {
		t.Fatalf("change event: %s", msgs)
	}
	if got := js(t, tab, `const reg = await navigator.serviceWorker.ready; return (await reg.pushManager.getSubscription()).endpoint;`); got != ch[0].NewEndpoint {
		t.Fatalf("the new subscription is not the current one: %s", got)
	}
}
