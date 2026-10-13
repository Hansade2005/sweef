package pushd

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
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/webpush"
	"github.com/sweefbrowser/sweef/internal/webpush/autopush"
	"golang.org/x/net/websocket"
)

// ---- an application server written without webpush.Encrypt: only the standard library --------------------------------

func hkdfSHA256(salt, ikm, info []byte, n int) []byte {
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

// appServerEncrypt is RFC 8291 section 3 for a single record, from the text of the RFC.
func appServerEncrypt(t testing.TB, uaPub, auth, msg []byte) []byte {
	t.Helper()
	curve := ecdh.P256()
	asKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uaKey, err := curve.NewPublicKey(uaPub)
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
	ikm := hkdfSHA256(auth, secret, append(append([]byte("WebPush: info\x00"), uaPub...), asPub...), 32)
	cek := hkdfSHA256(salt, ikm, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfSHA256(salt, ikm, []byte("Content-Encoding: nonce\x00"), 12)
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	ct := gcm.Seal(nil, nonce, append(append([]byte{}, msg...), 0x02), nil)
	hdr := append(append([]byte{}, salt...), 0, 0, 0, 0, byte(len(asPub)))
	binary.BigEndian.PutUint32(hdr[16:20], 4096)
	return append(append(hdr, asPub...), ct...)
}

// ---- a browser: handler plus transport --------------------------------------------------------------------------------

type browser struct {
	t      *testing.T
	keys   *webpush.Keys
	msgs   chan webpush.Message
	resets chan string
	uaid   atomic.Value
	chs    []string
	mu     sync.Mutex
	cl     *autopush.Client
}

func newBrowser(t *testing.T) *browser {
	k, err := webpush.NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, keys: k, msgs: make(chan webpush.Message, 16), resets: make(chan string, 4)}
}

func (b *browser) OnMessage(m webpush.Message) { b.msgs <- m }
func (b *browser) OnReset(u string)            { b.resets <- u }
func (b *browser) OnIdentity(u string)         { b.uaid.Store(u) }
func (b *browser) Channels() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.chs...)
}

func (b *browser) connect(srv *httptest.Server, uaid string) *autopush.Client {
	cl, err := autopush.New(autopush.Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", UAID: uaid, Handler: b})
	if err != nil {
		b.t.Fatal(err)
	}
	b.cl = cl
	b.t.Cleanup(func() { cl.Close() })
	return cl
}

func (b *browser) uaidStr() string {
	s, _ := b.uaid.Load().(string)
	return s
}

func (b *browser) subscribe(key []byte, chid string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ep, err := b.cl.Register(ctx, chid, key)
	if err != nil {
		b.t.Fatalf("register: %v", err)
	}
	b.mu.Lock()
	b.chs = append(b.chs, chid)
	b.mu.Unlock()
	return ep
}

func (b *browser) next(d time.Duration) (webpush.Message, bool) {
	select {
	case m := <-b.msgs:
		return m, true
	case <-time.After(d):
		return webpush.Message{}, false
	}
}

func startRelay(t *testing.T, cfg Config) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })
	return s, srv
}

func post(t testing.TB, ep string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", ep, bytes.NewReader(body))
	if len(body) > 0 {
		req.Header.Set("Content-Encoding", "aes128gcm")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// ---- tests ------------------------------------------------------------------------------------------------------------------

func TestEndToEndDeliveryDecryptsOnTheBrowser(t *testing.T) {
	_, srv := startRelay(t, Config{})
	b := newBrowser(t)
	b.connect(srv, "")
	vk, _ := webpush.NewVAPIDKey()
	ep := b.subscribe(vk.Public(), "chan-1")
	if !strings.HasPrefix(ep, srv.URL+"/wpush/v2/") {
		t.Fatalf("endpoint = %q", ep)
	}
	auth, err := vk.Authorization(srv.URL, "mailto:ops@example.com", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	body := appServerEncrypt(t, b.keys.P256dh(), b.keys.Auth, []byte(`{"title":"hello push"}`))
	resp := post(t, ep, body, map[string]string{"TTL": "60", "Authorization": auth, "Urgency": "high"})
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, srv.URL+"/wpush/v2/m/") {
		t.Fatalf("Location = %q", loc)
	}
	m, ok := b.next(5 * time.Second)
	if !ok {
		t.Fatal("the browser received nothing")
	}
	pt, err := b.keys.Decrypt(m.Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != `{"title":"hello push"}` || m.ChannelID != "chan-1" {
		t.Fatalf("message = %q on %q", pt, m.ChannelID)
	}
	b.cl.Ack(m.ChannelID, m.ID, webpush.AckDelivered)
}

func TestOfflineDeliveryAfterReconnectAndAck(t *testing.T) {
	s, srv := startRelay(t, Config{})
	b := newBrowser(t)
	cl := b.connect(srv, "")
	ep := b.subscribe(nil, "c1")
	uaid := cl.UAID()
	cl.Close() // the browser goes offline
	waitFor(t, func() bool { return s.Stats().Connected == 0 })
	for i := 0; i < 3; i++ {
		if r := post(t, ep, appServerEncrypt(t, b.keys.P256dh(), b.keys.Auth, []byte("m"+strconv.Itoa(i))), map[string]string{"TTL": "3600"}); r.StatusCode != 201 {
			t.Fatalf("post %d: %d", i, r.StatusCode)
		}
	}
	if st := s.Stats(); st.Stored != 3 {
		t.Fatalf("stored = %d", st.Stored)
	}
	b.connect(srv, uaid) // comes back with its identity
	var got []string
	for i := 0; i < 3; i++ {
		m, ok := b.next(5 * time.Second)
		if !ok {
			t.Fatalf("only %d messages arrived", i)
		}
		pt, err := b.keys.Decrypt(m.Data)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(pt))
		b.cl.Ack(m.ChannelID, m.ID, webpush.AckDelivered)
	}
	if strings.Join(got, ",") != "m0,m1,m2" {
		t.Fatalf("order = %v", got)
	}
	waitFor(t, func() bool { return s.Stats().Stored == 0 })
	select {
	case <-b.resets:
		t.Fatal("a known identity must not be reset")
	default:
	}
}

func TestUnacknowledgedMessagesAreRedelivered(t *testing.T) {
	_, srv := startRelay(t, Config{})
	b := newBrowser(t)
	cl := b.connect(srv, "")
	ep := b.subscribe(nil, "c1")
	post(t, ep, appServerEncrypt(t, b.keys.P256dh(), b.keys.Auth, []byte("x")), map[string]string{"TTL": "60"})
	if _, ok := b.next(5 * time.Second); !ok {
		t.Fatal("no first delivery")
	}
	uaid := cl.UAID()
	cl.Close() // no ack was sent
	b.connect(srv, uaid)
	if _, ok := b.next(5 * time.Second); !ok {
		t.Fatal("a message that was not acknowledged must be delivered again")
	}
}

func TestTTLExpiryAndZeroTTL(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Unix(1_800_000_000, 0).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	s, srv := startRelay(t, Config{Now: now})
	b := newBrowser(t)
	cl := b.connect(srv, "")
	ep := b.subscribe(nil, "c1")
	uaid := cl.UAID()
	cl.Close()
	waitFor(t, func() bool { return s.Stats().Connected == 0 })
	enc := func(s string) []byte { return appServerEncrypt(t, b.keys.P256dh(), b.keys.Auth, []byte(s)) }
	if r := post(t, ep, enc("short"), map[string]string{"TTL": "30"}); r.StatusCode != 201 {
		t.Fatal(r.StatusCode)
	}
	if r := post(t, ep, enc("long"), map[string]string{"TTL": "3600"}); r.StatusCode != 201 {
		t.Fatal(r.StatusCode)
	}
	if r := post(t, ep, enc("now or never"), map[string]string{"TTL": "0"}); r.StatusCode != 201 {
		t.Fatalf("TTL 0 is accepted and dropped for an offline browser: %d", r.StatusCode)
	}
	if st := s.Stats(); st.Stored != 2 {
		t.Fatalf("stored = %d (TTL 0 must not be stored)", st.Stored)
	}
	clock.Add(int64(time.Minute))
	s.Sweep()
	if st := s.Stats(); st.Stored != 1 || st.Expired != 1 {
		t.Fatalf("after the short TTL: %+v", st)
	}
	b.connect(srv, uaid)
	m, ok := b.next(5 * time.Second)
	if !ok {
		t.Fatal("the long message must still arrive")
	}
	if pt, _ := b.keys.Decrypt(m.Data); string(pt) != "long" {
		t.Fatalf("got %q", pt)
	}
	if _, ok := b.next(300 * time.Millisecond); ok {
		t.Fatal("only one message was alive")
	}
	// TTL 0 to a connected browser is delivered
	post(t, ep, enc("live"), map[string]string{"TTL": "0"})
	if m, ok := b.next(5 * time.Second); !ok {
		t.Fatal("TTL 0 must reach a connected browser")
	} else if pt, _ := b.keys.Decrypt(m.Data); string(pt) != "live" {
		t.Fatalf("got %q", pt)
	}
}

func TestMaxTTLIsClamped(t *testing.T) {
	_, srv := startRelay(t, Config{MaxTTL: time.Hour})
	b := newBrowser(t)
	b.connect(srv, "")
	ep := b.subscribe(nil, "c1")
	r := post(t, ep, nil, map[string]string{"TTL": "999999"})
	if r.StatusCode != 201 || r.Header.Get("TTL") != "3600" {
		t.Fatalf("status %d TTL %q", r.StatusCode, r.Header.Get("TTL"))
	}
}

func TestStatusCodes(t *testing.T) {
	s, srv := startRelay(t, Config{})
	b := newBrowser(t)
	b.connect(srv, "")
	ep := b.subscribe(nil, "c1")
	body := appServerEncrypt(t, b.keys.P256dh(), b.keys.Auth, []byte("x"))

	forged := ep[:len(ep)-2] + "AA"
	if ep[len(ep)-2:] == "AA" {
		forged = ep[:len(ep)-2] + "BB"
	}
	other, _ := New(Config{Secret: bytes.Repeat([]byte{7}, 32)})
	defer other.Close()
	foreignToken := other.token(strings.Repeat("ab", 16))
	for name, c := range map[string]struct {
		url  string
		body []byte
		hdr  map[string]string
		want int
	}{
		"missing TTL":          {ep, body, map[string]string{"TTL": ""}, 400},
		"bad TTL":              {ep, body, map[string]string{"TTL": "abc"}, 400},
		"negative TTL":         {ep, body, map[string]string{"TTL": "-1"}, 400},
		"bad urgency":          {ep, body, map[string]string{"TTL": "5", "Urgency": "now"}, 400},
		"bad topic":            {ep, body, map[string]string{"TTL": "5", "Topic": "has space"}, 400},
		"long topic":           {ep, body, map[string]string{"TTL": "5", "Topic": strings.Repeat("a", 33)}, 400},
		"wrong content coding": {ep, body, map[string]string{"TTL": "5", "Content-Encoding": "aesgcm"}, 400},
		"too large":            {ep, make([]byte, 4097), map[string]string{"TTL": "5"}, 413},
		"largest":              {ep, make([]byte, 4096), map[string]string{"TTL": "5"}, 201},
		"forged mac":           {forged, body, map[string]string{"TTL": "5"}, 404},
		"foreign token":        {srv.URL + "/wpush/v2/" + foreignToken, body, map[string]string{"TTL": "5"}, 404},
		"garbage token":        {srv.URL + "/wpush/v2/nonsense", body, map[string]string{"TTL": "5"}, 404},
		"ok":                   {ep, body, map[string]string{"TTL": "5", "Urgency": "very-low", "Topic": "abc_DEF-1"}, 201},
		"no payload":           {ep, nil, map[string]string{"TTL": "5"}, 201},
	} {
		if r := post(t, c.url, c.body, c.hdr); r.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", name, r.StatusCode, c.want)
		}
	}
	if r, _ := http.Get(ep); r.StatusCode != 405 {
		t.Errorf("GET: %d", r.StatusCode)
	}
	if !s.EndpointValid(ep) || s.EndpointValid(forged) || s.EndpointValid(srv.URL+"/wpush/v2/"+foreignToken) {
		t.Error("EndpointValid disagrees with the status codes")
	}

	// unsubscribe: the endpoint is 410 afterwards, the token is still genuine
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.cl.Unregister(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if r := post(t, ep, body, map[string]string{"TTL": "5"}); r.StatusCode != 410 {
		t.Errorf("after unregister: %d, want 410", r.StatusCode)
	}
	if st := s.Stats(); st.Channels != 0 || st.Stored != 0 {
		t.Errorf("unregister must drop the channel and its messages: %+v", st)
	}
}

func TestTopicReplacesAndDeleteWithdraws(t *testing.T) {
	s, srv := startRelay(t, Config{})
	b := newBrowser(t)
	cl := b.connect(srv, "")
	ep := b.subscribe(nil, "c1")
	uaid := cl.UAID()
	cl.Close()
	waitFor(t, func() bool { return s.Stats().Connected == 0 })
	enc := func(s string) []byte { return appServerEncrypt(t, b.keys.P256dh(), b.keys.Auth, []byte(s)) }
	post(t, ep, enc("v1"), map[string]string{"TTL": "600", "Topic": "score"})
	post(t, ep, enc("v2"), map[string]string{"TTL": "600", "Topic": "score"})
	r := post(t, ep, enc("other"), map[string]string{"TTL": "600"})
	if st := s.Stats(); st.Stored != 2 {
		t.Fatalf("a topic must replace the waiting message: stored = %d", st.Stored)
	}
	req, _ := http.NewRequest("DELETE", r.Header.Get("Location"), nil)
	d, err := http.DefaultClient.Do(req)
	if err != nil || d.StatusCode != 204 {
		t.Fatalf("DELETE: %v %v", err, d)
	}
	d2, _ := http.DefaultClient.Do(req)
	if d2.StatusCode != 404 {
		t.Fatalf("second DELETE: %d", d2.StatusCode)
	}
	bad, _ := http.NewRequest("DELETE", srv.URL+"/wpush/v2/m/forged", nil)
	if d3, _ := http.DefaultClient.Do(bad); d3.StatusCode != 404 {
		t.Fatalf("forged message resource: %d", d3.StatusCode)
	}
	b.connect(srv, uaid)
	m, _ := b.next(5 * time.Second)
	if pt, _ := b.keys.Decrypt(m.Data); string(pt) != "v2" {
		t.Fatalf("got %q, want the latest of the topic", pt)
	}
	if _, ok := b.next(300 * time.Millisecond); ok {
		t.Fatal("the withdrawn message was delivered")
	}
}

func TestVAPIDIsEnforcedForKeyedSubscriptions(t *testing.T) {
	_, srv := startRelay(t, Config{})
	b := newBrowser(t)
	b.connect(srv, "")
	good, _ := webpush.NewVAPIDKey()
	other, _ := webpush.NewVAPIDKey()
	ep := b.subscribe(good.Public(), "c1")
	plain := b.subscribe(nil, "c2")
	hdr := func(k *webpush.VAPIDKey, aud string, exp time.Time) map[string]string {
		a, _ := k.Authorization(aud, "mailto:a@example.com", exp)
		return map[string]string{"TTL": "5", "Authorization": a}
	}
	exp := time.Now().Add(time.Hour)
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"valid", hdr(good, srv.URL, exp), 201},
		{"missing", map[string]string{"TTL": "5"}, 401},
		{"other key", hdr(other, srv.URL, exp), 403},
		{"wrong audience", hdr(good, "https://elsewhere.example", exp), 401},
		{"expired", hdr(good, srv.URL, time.Now().Add(-time.Minute)), 401},
		{"too far ahead", hdr(good, srv.URL, time.Now().Add(48*time.Hour)), 401},
		{"garbage", map[string]string{"TTL": "5", "Authorization": "vapid t=x.y.z, k=AAAA"}, 401},
	}
	for _, c := range cases {
		if r := post(t, ep, nil, c.hdr); r.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, r.StatusCode, c.want)
		}
	}
	if r := post(t, plain, nil, map[string]string{"TTL": "5"}); r.StatusCode != 201 {
		t.Errorf("a subscription without key needs no VAPID: %d", r.StatusCode)
	}
	if r := post(t, plain, nil, hdr(other, srv.URL, exp)); r.StatusCode != 201 {
		t.Errorf("a valid VAPID header on an unkeyed subscription: %d", r.StatusCode)
	}
	if r := post(t, plain, nil, map[string]string{"TTL": "5", "Authorization": "vapid t=x.y.z, k=AAAA"}); r.StatusCode != 401 {
		t.Errorf("a broken VAPID header is refused even when none is needed: %d", r.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Unix(1_800_000_000, 0).UnixNano())
	_, srv := startRelay(t, Config{Now: func() time.Time { return time.Unix(0, clock.Load()) }, IPRate: 1, IPBurst: 3, EndpointRate: 100, EndpointBurst: 100})
	b := newBrowser(t)
	b.connect(srv, "")
	ep := b.subscribe(nil, "c1")
	got := map[int]int{}
	for i := 0; i < 6; i++ {
		got[post(t, ep, nil, map[string]string{"TTL": "5"}).StatusCode]++
	}
	if got[201] != 3 || got[429] != 3 {
		t.Fatalf("burst of 3 then limited: %v", got)
	}
	clock.Add(int64(2 * time.Second))
	if r := post(t, ep, nil, map[string]string{"TTL": "5"}); r.StatusCode != 201 || post(t, ep, nil, map[string]string{"TTL": "5"}).StatusCode != 201 {
		t.Fatal("the bucket must refill with time")
	} else if r2 := post(t, ep, nil, map[string]string{"TTL": "5"}); r2.StatusCode != 429 || r2.Header.Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d", r2.StatusCode)
	}
}

func TestStoreIsBounded(t *testing.T) {
	s, srv := startRelay(t, Config{MaxQueuePerClient: 3, MaxStoredBytes: 100, IPRate: 1000, IPBurst: 1000, EndpointRate: 1000, EndpointBurst: 1000})
	b := newBrowser(t)
	cl := b.connect(srv, "")
	ep := b.subscribe(nil, "c1")
	cl.Close()
	waitFor(t, func() bool { return s.Stats().Connected == 0 })
	for i := 0; i < 5; i++ {
		if r := post(t, ep, bytes.Repeat([]byte{'a'}, 10), map[string]string{"TTL": "60"}); r.StatusCode != 201 {
			t.Fatal(r.StatusCode)
		}
	}
	if st := s.Stats(); st.Stored != 3 || st.StoredBytes != 30 {
		t.Fatalf("queue bound: %+v", st)
	}
	if r := post(t, ep, bytes.Repeat([]byte{'a'}, 80), map[string]string{"TTL": "60"}); r.StatusCode != 503 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("byte bound: %d", r.StatusCode)
	}
}

func TestRestartKeepsEndpointsAndMessages(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(Config{DataDir: dir, PublicURL: "http://relay.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	srv1 := httptest.NewServer(s1.Handler())
	b := newBrowser(t)
	cl := b.connect(srv1, "")
	ep := b.subscribe(nil, "c1")
	uaid := cl.UAID()
	cl.Close()
	if !strings.HasPrefix(ep, "http://relay.invalid/wpush/v2/") {
		t.Fatalf("PublicURL ignored: %s", ep)
	}
	real := srv1.URL + strings.TrimPrefix(ep, "http://relay.invalid")
	waitFor(t, func() bool { return s1.Stats().Connected == 0 })
	post(t, real, appServerEncrypt(t, b.keys.P256dh(), b.keys.Auth, []byte("survives")), map[string]string{"TTL": "600"})
	srv1.Close()
	s1.Close()

	s2, err := New(Config{DataDir: dir, PublicURL: "http://relay.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(s2.Handler())
	t.Cleanup(func() { srv2.Close(); s2.Close() })
	if !s2.EndpointValid(ep) {
		t.Fatal("endpoints must stay valid across a restart (secret persisted)")
	}
	b.connect(srv2, uaid)
	m, ok := b.next(5 * time.Second)
	if !ok {
		t.Fatal("the stored message did not survive the restart")
	}
	if pt, _ := b.keys.Decrypt(m.Data); string(pt) != "survives" {
		t.Fatalf("got %q", pt)
	}
	// a relay with a different secret rejects the old endpoint
	s3, _ := New(Config{})
	defer s3.Close()
	if s3.EndpointValid(ep) {
		t.Fatal("a fresh secret must not accept old endpoints")
	}
}

func TestUnknownIdentityIsReset(t *testing.T) {
	_, srv := startRelay(t, Config{})
	b := newBrowser(t)
	b.connect(srv, "deadbeefdeadbeefdeadbeefdeadbeef") // an identity this relay never issued
	select {
	case u := <-b.resets:
		if u == "" || u == "deadbeefdeadbeefdeadbeefdeadbeef" {
			t.Fatalf("new uaid = %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnReset was not called for an unknown identity")
	}
}

func TestProtocolRules(t *testing.T) {
	_, srv := startRelay(t, Config{})
	dial := func() *websocket.Conn {
		c, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "", "http://localhost/")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// register before hello closes the connection
	c := dial()
	websocket.Message.Send(c, `{"messageType":"register","channelID":"x"}`)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var s string
	if err := websocket.Message.Receive(c, &s); err == nil {
		t.Fatalf("expected the relay to hang up, got %q", s)
	}
	// ping, duplicate register, invalid ids and keys
	c = dial()
	defer c.Close()
	rt := func(msg string) string {
		websocket.Message.Send(c, msg)
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var r string
		if err := websocket.Message.Receive(c, &r); err != nil {
			t.Fatalf("%s: %v", msg, err)
		}
		return r
	}
	if r := rt(`{"messageType":"hello","use_webpush":true}`); !strings.Contains(r, `"status":200`) {
		t.Fatal(r)
	}
	if r := rt(`{}`); r != `{}` {
		t.Fatalf("ping reply = %q", r)
	}
	if r := rt(`{"messageType":"register","channelID":"a b"}`); !strings.Contains(r, `"status":401`) {
		t.Fatalf("bad channel id: %s", r)
	}
	if r := rt(`{"messageType":"register","channelID":"ok","key":"AAAA"}`); !strings.Contains(r, `"status":400`) {
		t.Fatalf("bad key: %s", r)
	}
	if r := rt(`{"messageType":"register","channelID":"ok"}`); !strings.Contains(r, `"status":200`) || !strings.Contains(r, "pushEndpoint") {
		t.Fatalf("register: %s", r)
	}
	if r := rt(`{"messageType":"register","channelID":"ok"}`); !strings.Contains(r, `"status":409`) {
		t.Fatalf("duplicate: %s", r)
	}
}

func TestClientReconnectsWithBackoffAndKeepsAlive(t *testing.T) {
	_, srv := startRelay(t, Config{})
	var mu sync.Mutex
	var waits []time.Duration
	var dials atomic.Int32
	b := newBrowser(t)
	cl, err := autopush.New(autopush.Config{
		URL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", Handler: b, MinBackoff: time.Second, MaxBackoff: 8 * time.Second,
		After: func(d time.Duration) <-chan time.Time {
			if d >= time.Second {
				mu.Lock()
				waits = append(waits, d)
				mu.Unlock()
			}
			c := make(chan time.Time, 1)
			if d < 4*time.Minute { // backoff waits elapse at once; pings never fire in this test
				c <- time.Now()
			}
			return c
		},
		Dial: func(ctx context.Context, u string) (*websocket.Conn, error) {
			if n := dials.Add(1); n <= 5 {
				return nil, context.DeadlineExceeded
			}
			return websocket.Dial(u, "", "http://localhost/")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	waitFor(t, cl.Connected)
	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	if len(waits) < len(want) {
		t.Fatalf("waits = %v", waits)
	}
	for i, w := range want {
		if waits[i] != w {
			t.Fatalf("backoff %d = %v, want %v (all: %v)", i, waits[i], w, waits)
		}
	}
}

func TestRegisterFailsWithoutConnection(t *testing.T) {
	b := newBrowser(t)
	cl, err := autopush.New(autopush.Config{URL: "ws://127.0.0.1:1/ws", Handler: b, MinBackoff: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := cl.Register(ctx, "c", nil); err == nil {
		t.Fatal("register must fail when the service cannot be reached")
	}
	if _, err := autopush.New(autopush.Config{URL: "http://x", Handler: b}); err == nil {
		t.Fatal("a non-websocket URL must be refused")
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
