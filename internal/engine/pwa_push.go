package engine

// pwa_push.go: Web Push in the browser. A subscription belongs to a service worker registration. Subscribing
// creates a receiving key pair and auth secret, registers a channel with the push service and returns the endpoint
// the application server posts to. Messages arrive over the service connection (webpush.Transport), are decrypted
// (RFC 8291) and handed to the registration's worker as a `push` event. A subscription that is lost (the service forgot
// this browser) produces `pushsubscriptionchange`. userVisibleOnly is enforced: a push that does not end in a
// notification gets a generic one after a grace period.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/webpush"
	"github.com/sweefbrowser/sweef/internal/webpush/autopush"
)

// pushSub is one push subscription.
type pushSub struct {
	Scope    string `json:"scope"`
	Channel  string `json:"channel"`
	Endpoint string `json:"endpoint"`
	Priv     []byte `json:"priv"`
	Auth     []byte `json:"auth"`
	AppKey   []byte `json:"appKey,omitempty"`
	Created  int64  `json:"created"`

	origin string
	keys   *webpush.Keys
	mu     sync.Mutex // one message at a time per subscription, in arrival order
}

func (s *pushSub) desc() map[string]any {
	var key any
	if len(s.AppKey) > 0 {
		key = webpush.B64(s.AppKey)
	}
	return map[string]any{"endpoint": s.Endpoint, "p256dh": webpush.B64(s.keys.P256dh()), "auth": webpush.B64(s.Auth), "appKey": key, "channel": s.Channel}
}

func (s *pushSub) prepare(origin string) error {
	k, err := ecdh.P256().NewPrivateKey(s.Priv)
	if err != nil {
		return err
	}
	s.origin = origin
	s.keys = &webpush.Keys{Private: k, Auth: s.Auth}
	return nil
}

type pushIdentity struct {
	Service string `json:"service"`
	UAID    string `json:"uaid"`
}

type pushSvc struct {
	st      *pwaState
	mu      sync.Mutex
	service string
	tr      webpush.Transport
	byChan  map[string]*pushSub
	id      pushIdentity
	loaded  bool
	queue   chan webpush.Message
	quit    chan struct{}
}

func (st *pwaState) pushURL() string {
	if st.o.PushService != "" {
		return st.o.PushService
	}
	return os.Getenv("SWEEF_PUSH_SERVICE")
}

// pushEnabled reports whether a push service (or a replacement transport) is configured.
func (st *pwaState) pushEnabled() bool { return st.o.PushTransport != nil || st.pushURL() != "" }

func wsURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("push service %q is not a URL", raw)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("push service %q must be an http(s) or ws(s) URL", raw)
	}
	return u.String(), nil
}

func (st *pwaState) svc() *pushSvc {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.push == nil {
		st.push = &pushSvc{st: st, byChan: map[string]*pushSub{}, queue: make(chan webpush.Message, 256), quit: make(chan struct{})}
	}
	return st.push
}

func (p *pushSvc) identityFile() string {
	root := p.st.c.SiteStorage().Root()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "push-service.json")
}

// load reads the identity and every persisted subscription once.
func (p *pushSvc) load() {
	p.mu.Lock()
	if p.loaded {
		p.mu.Unlock()
		return
	}
	p.loaded = true
	p.service = p.st.pushURL()
	if f := p.identityFile(); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			_ = json.Unmarshal(b, &p.id)
		}
	}
	p.mu.Unlock()
	for _, o := range p.st.allOrigins() {
		d := p.st.originData(o)
		p.st.mu.Lock()
		for _, s := range d.Push {
			if s.prepare(o) == nil {
				p.mu.Lock()
				p.byChan[s.Channel] = s
				p.mu.Unlock()
			}
		}
		p.st.mu.Unlock()
	}
	// subscriptions made against another service are of no use to this one
	p.mu.Lock()
	stale := p.id.Service != "" && p.id.Service != p.service && p.service != ""
	p.mu.Unlock()
	if stale {
		p.mu.Lock()
		p.id = pushIdentity{Service: p.service}
		p.mu.Unlock()
		p.st.goJob(p.resetAll)
	}
}

func (p *pushSvc) saveIdentity() {
	f := p.identityFile()
	if f == "" {
		return
	}
	p.mu.Lock()
	b, _ := json.Marshal(p.id)
	p.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(f), 0o700)
	tmp := f + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, f)
	}
}

// startPush connects at start-up when subscriptions exist, so that pushes arrive while the browser runs.
func (st *pwaState) startPush() {
	if !st.pushEnabled() {
		return
	}
	st.goJob(func() {
		p := st.svc()
		p.load()
		p.mu.Lock()
		n := len(p.byChan)
		p.mu.Unlock()
		if n > 0 {
			_, _ = p.transport()
		}
	})
}

func (st *pwaState) stopPush() {
	st.mu.Lock()
	p := st.push
	st.mu.Unlock()
	if p == nil {
		return
	}
	p.mu.Lock()
	tr := p.tr
	p.tr = nil
	select {
	case <-p.quit:
	default:
		close(p.quit)
	}
	p.mu.Unlock()
	if tr != nil {
		tr.Close()
	}
}

// pushHandler adapts the service connection's callbacks.
type pushHandler struct{ p *pushSvc }

func (h pushHandler) OnMessage(m webpush.Message) {
	select {
	case h.p.queue <- m:
	default: // too many unprocessed messages: do not acknowledge, the service keeps it
	}
}
func (h pushHandler) OnIdentity(uaid string) {
	h.p.mu.Lock()
	h.p.id.UAID = uaid
	h.p.id.Service = h.p.service
	h.p.mu.Unlock()
	h.p.saveIdentity()
}
func (h pushHandler) OnReset(newUAID string) { h.p.st.goJob(h.p.resetAll) }
func (h pushHandler) Channels() []string {
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	out := make([]string, 0, len(h.p.byChan))
	for c := range h.p.byChan {
		out = append(out, c)
	}
	return out
}

// transport returns the service connection, creating it on first use.
func (p *pushSvc) transport() (webpush.Transport, error) {
	p.mu.Lock()
	if p.tr != nil {
		tr := p.tr
		p.mu.Unlock()
		return tr, nil
	}
	uaid := p.id.UAID
	if p.id.Service != "" && p.id.Service != p.service {
		uaid = ""
	}
	p.mu.Unlock()
	var tr webpush.Transport
	var err error
	if f := p.st.o.PushTransport; f != nil {
		tr, err = f(pushHandler{p}, uaid)
	} else {
		var u string
		if u, err = wsURL(p.service); err == nil {
			tr, err = autopush.New(autopush.Config{URL: u, UAID: uaid, Handler: pushHandler{p}, Logf: p.st.o.Logf})
		}
	}
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.tr != nil { // lost a race
		old := p.tr
		p.mu.Unlock()
		tr.Close()
		return old, nil
	}
	p.tr = tr
	p.mu.Unlock()
	p.st.goJob(p.pump)
	return tr, nil
}

// pump processes incoming messages one after the other.
func (p *pushSvc) pump() {
	for {
		select {
		case m := <-p.queue:
			p.deliver(m)
		case <-p.quit:
			return
		case <-p.st.quit:
			return
		}
	}
}

func (p *pushSvc) ack(m webpush.Message, code webpush.AckCode) {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr != nil {
		tr.Ack(m.ChannelID, m.ID, code)
	}
}

// deliver turns one message of the service into a push event.
func (p *pushSvc) deliver(m webpush.Message) {
	st := p.st
	p.mu.Lock()
	sub := p.byChan[m.ChannelID]
	p.mu.Unlock()
	if sub == nil {
		p.ack(m, webpush.AckNoSubscriber)
		return
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if st.permission(sub.origin, PermPush) != "granted" {
		p.ack(m, webpush.AckDelivered) // dropped: the user does not want pushes from this site
		return
	}
	var data []byte
	if m.Data != nil {
		var err error
		if data, err = sub.keys.Decrypt(m.Data); err != nil {
			st.o.Logf("pwa: push for %s: %v", sub.origin, err)
			p.ack(m, webpush.AckDecryptError)
			return
		}
	}
	msgr := st.c.sw()
	msgr.ensure(sub.origin)
	msgr.mu.Lock()
	reg := msgr.regs[regKey(sub.origin, sub.Scope)]
	msgr.mu.Unlock()
	if reg == nil {
		p.dropSub(sub, true)
		p.ack(m, webpush.AckNoSubscriber)
		return
	}
	st.mu.Lock()
	before := st.shown[sub.origin]
	st.mu.Unlock()
	payload := map[string]any{"hasData": m.Data != nil}
	if data != nil {
		payload["data"] = webpush.B64(data)
	}
	_, err := msgr.dispatchReg(reg, "push", payload, st.o.EventTimeout)
	if err != nil {
		st.o.Logf("pwa: push event for %s: %v", sub.origin, err)
	}
	p.ack(m, webpush.AckDelivered)
	// userVisibleOnly: the push must have produced a notification (the worker had its chance during the event)
	origin, scope := sub.origin, sub.Scope
	st.clock.AfterFunc(st.o.SilentPushGrace, func() {
		st.goJob(func() {
			st.mu.Lock()
			now := st.shown[origin]
			st.mu.Unlock()
			if now == before {
				st.genericNotification(origin, scope)
			}
		})
	})
}

// genericNotification is shown when a push did not show anything: the site must not use pushes silently.
func (st *pwaState) genericNotification(origin, scope string) {
	if st.o.Sink == nil || st.permission(origin, PermNotifications) != "granted" {
		return
	}
	n := &pwaNote{origin: origin, tag: "user_visible_auto_notification", auto: true, persistent: true, scope: scope,
		req: noteReq{Title: originHost(origin), Body: "This site has been updated in the background.", Tag: "user_visible_auto_notification"}}
	if _, err := st.registerAndShow(n, ""); err != nil {
		st.o.Logf("pwa: generic notification: %v", err)
	}
}

// ---- subscribe, query, unsubscribe -------------------------------------------------------------------------------------

func (p *pushSvc) find(origin, scope string) *pushSub {
	d := p.st.originData(origin)
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	for _, s := range d.Push {
		if s.Scope == scope {
			return s
		}
	}
	return nil
}

func (p *pushSvc) subscribe(origin string, reg *swReg, appKey []byte, tab *Tab, gesture bool) (*pushSub, error) {
	st := p.st
	m := st.c.sw()
	m.mu.Lock()
	active := reg.active != nil
	m.mu.Unlock()
	if !active {
		return nil, &swError{"InvalidStateError", "Subscription failed - no active Service Worker"}
	}
	if !st.enabled.Load() {
		return nil, &swError{"NotAllowedError", "Registration failed - permission denied"}
	}
	p.load()
	if old := p.find(origin, reg.scope); old != nil {
		if len(appKey) == 0 || bytes.Equal(old.AppKey, appKey) {
			return old, nil
		}
		return nil, &swError{"InvalidStateError", "Subscription failed - a subscription with a different applicationServerKey already exists; to change the applicationServerKey, unsubscribe then resubscribe."}
	}
	if !st.pushEnabled() {
		return nil, &swError{"AbortError", "Registration failed - push service not available"}
	}
	switch st.permission(origin, PermPush) {
	case "denied":
		return nil, &swError{"NotAllowedError", "Registration failed - permission denied"}
	case "prompt":
		tabID := ""
		if tab != nil {
			tabID = tab.ID
		}
		if st.request(origin, PermNotifications, tabID, gesture) != "granted" {
			return nil, &swError{"NotAllowedError", "Registration failed - permission denied"}
		}
	}
	tr, err := p.transport()
	if err != nil {
		return nil, &swError{"AbortError", "Registration failed - push service error: " + err.Error()}
	}
	keys, err := webpush.NewKeys()
	if err != nil {
		return nil, &swError{"AbortError", "Registration failed - " + err.Error()}
	}
	chid := newChannelID()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	endpoint, err := tr.Register(ctx, chid, appKey)
	if err != nil {
		if errors.Is(err, webpush.ErrUnavailable) {
			return nil, &swError{"AbortError", "Registration failed - could not reach the push service"}
		}
		return nil, &swError{"AbortError", "Registration failed - " + err.Error()}
	}
	sub := &pushSub{Scope: reg.scope, Channel: chid, Endpoint: endpoint, Priv: keys.Private.Bytes(), Auth: keys.Auth, AppKey: appKey, Created: time.Now().Unix()}
	if err := sub.prepare(origin); err != nil {
		return nil, &swError{"AbortError", "Registration failed - " + err.Error()}
	}
	d := st.originData(origin)
	st.mu.Lock()
	d.Push = append(d.Push, sub)
	st.saveOriginLocked(origin)
	st.mu.Unlock()
	p.mu.Lock()
	p.byChan[chid] = sub
	p.mu.Unlock()
	return sub, nil
}

func newChannelID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// dropSub forgets a subscription; unregister also tells the service so its endpoint answers 410.
func (p *pushSvc) dropSub(s *pushSub, unregister bool) {
	st := p.st
	d := st.originData(s.origin)
	st.mu.Lock()
	out := d.Push[:0]
	for _, o := range d.Push {
		if o != s {
			out = append(out, o)
		}
	}
	d.Push = out
	st.saveOriginLocked(s.origin)
	st.mu.Unlock()
	p.mu.Lock()
	delete(p.byChan, s.Channel)
	tr := p.tr
	p.mu.Unlock()
	if unregister && tr != nil {
		st.goJob(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = tr.Unregister(ctx, s.Channel)
		})
	}
}

func (st *pwaState) revokePush(origin string) {
	p := st.svc()
	d := st.originData(origin)
	st.mu.Lock()
	subs := append([]*pushSub(nil), d.Push...)
	st.mu.Unlock()
	for _, s := range subs {
		p.dropSub(s, true)
	}
}

func (st *pwaState) clearPush(origin string) { st.revokePush(origin) }

func (st *pwaState) dropPushOf(origin, scope string) {
	if st.push == nil && !st.pushEnabled() {
		return
	}
	p := st.svc()
	if s := p.find(origin, scope); s != nil {
		p.dropSub(s, true)
	}
}

// resetAll handles a service that no longer knows this browser: every subscription is gone, so each is replaced
// by a new one with the same options when that is possible, and the worker is told with pushsubscriptionchange.
func (p *pushSvc) resetAll() {
	st := p.st
	st.mu.Lock()
	var all []*pushSub
	for _, d := range st.data {
		all = append(all, d.Push...)
	}
	st.mu.Unlock()
	for _, old := range all {
		oldDesc := old.desc()
		p.dropSub(old, false)
		var newDesc any
		m := st.c.sw()
		m.ensure(old.origin)
		m.mu.Lock()
		reg := m.regs[regKey(old.origin, old.Scope)]
		m.mu.Unlock()
		if reg == nil {
			continue
		}
		if st.permission(old.origin, PermPush) == "granted" {
			if ns, err := p.subscribe(old.origin, reg, old.AppKey, nil, false); err == nil {
				newDesc = ns.desc()
			}
		}
		_, err := m.dispatchReg(reg, "pushsubscriptionchange", map[string]any{"old": oldDesc, "new": newDesc}, st.o.EventTimeout)
		if err != nil {
			st.o.Logf("pwa: pushsubscriptionchange for %s: %v", old.origin, err)
		}
	}
}

// ---- host functions ---------------------------------------------------------------------------------------------------------

func (h *pwaHost) pushFns(f map[string]hostFn) {
	f["pwaPushGet"] = func(a []any) (any, error) {
		st := h.st()
		reg := st.c.sw().regByID(argS(a, 0))
		if reg == nil || reg.origin != h.origin() || !st.pushEnabled() {
			return nil, nil
		}
		p := st.svc()
		p.load()
		if s := p.find(reg.origin, reg.scope); s != nil {
			return s.desc(), nil
		}
		return nil, nil
	}
	f["pwaPushAvailable"] = func(a []any) (any, error) { return h.st().pushEnabled(), nil }
	f["pwaPushSubscribe"] = func(a []any) (any, error) {
		token, regID, keyB64 := argI(a, 0), argS(a, 1), argS(a, 2)
		st := h.st()
		var key []byte
		if keyB64 != "" {
			b, err := webpush.DecodeB64(keyB64)
			if err == nil {
				_, err = webpush.ParsePublicKey(b)
			}
			if err != nil {
				return nil, &swError{"InvalidAccessError", "Failed to execute 'subscribe' on 'PushManager': The provided applicationServerKey is not valid."}
			}
			key = b
		}
		reg := st.c.sw().regByID(regID)
		if reg == nil || reg.origin != h.origin() {
			return nil, &swError{"InvalidStateError", "Subscription failed - no active Service Worker"}
		}
		tab, gesture := h.j.tab, h.j.tab.UserActive() && h.worker() == nil
		st.goJob(func() {
			sub, err := st.svc().subscribe(reg.origin, reg, key, tab, gesture)
			if err != nil {
				h.reply(token, nil, err)
				return
			}
			h.reply(token, sub.desc(), nil)
		})
		return nil, nil
	}
	f["pwaPushUnsubscribe"] = func(a []any) (any, error) {
		token := argI(a, 0)
		st := h.st()
		reg := st.c.sw().regByID(argS(a, 1))
		st.goJob(func() {
			ok := false
			if reg != nil && reg.origin == h.origin() && st.pushEnabled() {
				p := st.svc()
				p.load()
				if s := p.find(reg.origin, reg.scope); s != nil {
					p.dropSub(s, true)
					ok = true
				}
			}
			h.reply(token, ok, nil)
		})
		return nil, nil
	}
}
