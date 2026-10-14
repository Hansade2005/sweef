// Package pushd is a small self-hostable Web Push relay. It speaks the two halves of a push service:
//
//   - towards application servers, the HTTP Web Push protocol (RFC 8030) with VAPID (RFC 8292):
//     POST /wpush/v2/<token> with TTL, Urgency and Topic headers and an aes128gcm body (RFC 8291);
//   - towards browsers, the autopush WebSocket protocol (hello, register, unregister, notification, ack, ping).
//
// The relay never sees plaintext: bodies are opaque encrypted records that it stores (while the browser is
// offline, up to the message's TTL) and forwards. Endpoints are unguessable and unforgeable: an endpoint
// token is a random endpoint id followed by an HMAC of it under the relay's secret. See docs/push.md.
package pushd

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/webpush"
	"golang.org/x/net/websocket"
)

// Config configures a relay. The zero value works (memory only, random secret) except that endpoints then do not
// survive a restart.
type Config struct {
	// PublicURL is the externally visible base URL ("https://push.example.com"), used to build endpoints and as the
	// VAPID audience. Empty: derived from each request.
	PublicURL string
	// Secret keys the endpoint HMACs. Empty: loaded from (or created in) DataDir, else random for this process.
	Secret []byte
	// DataDir, when set, keeps registrations and undelivered messages across restarts (state.json, mode 0600).
	DataDir string

	MaxBody           int           // largest accepted request body (default 4096, RFC 8030 section 7.2)
	MaxTTL            time.Duration // longest retention (default 30 days)
	MaxQueuePerClient int           // stored messages per browser (default 100); the oldest is dropped beyond it
	MaxChannels       int           // subscriptions per browser (default 200)
	MaxStoredBytes    int           // all stored bodies together (default 64 MiB); beyond it POST answers 503
	ClientExpiry      time.Duration // forget a browser not seen for this long (default 60 days)
	IdleTimeout       time.Duration // close a WebSocket silent for this long (default 10 minutes)

	// Rate limits for application servers: requests per second and burst, per source address and per endpoint.
	IPRate, IPBurst             float64
	EndpointRate, EndpointBurst float64
	// TrustForwarded takes the client address from X-Forwarded-For (set it only behind your own proxy).
	TrustForwarded bool
	// RequireVAPID refuses posts without a valid VAPID header even for subscriptions made without a key.
	RequireVAPID bool

	Now  func() time.Time
	Logf func(format string, args ...any)
}

func (c *Config) defaults() {
	if c.MaxBody == 0 {
		c.MaxBody = webpush.MaxBodySize
	}
	if c.MaxTTL == 0 {
		c.MaxTTL = 30 * 24 * time.Hour
	}
	if c.MaxQueuePerClient == 0 {
		c.MaxQueuePerClient = 100
	}
	if c.MaxChannels == 0 {
		c.MaxChannels = 200
	}
	if c.MaxStoredBytes == 0 {
		c.MaxStoredBytes = 64 << 20
	}
	if c.ClientExpiry == 0 {
		c.ClientExpiry = 60 * 24 * time.Hour
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = 10 * time.Minute
	}
	if c.IPRate == 0 {
		c.IPRate, c.IPBurst = 50, 100
	}
	if c.EndpointRate == 0 {
		c.EndpointRate, c.EndpointBurst = 10, 30
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// ---- state ------------------------------------------------------------------------------------------------------

type channel struct {
	ID      string    `json:"id"`
	EID     string    `json:"eid"`
	Key     []byte    `json:"key,omitempty"`
	Created time.Time `json:"created"`
}

type stored struct {
	ID      string    `json:"id"`
	Channel string    `json:"channel"`
	Data    []byte    `json:"data,omitempty"`
	Expires time.Time `json:"expires"`
	Topic   string    `json:"topic,omitempty"`
	Urgency string    `json:"urgency,omitempty"`
	At      time.Time `json:"at"`
}

type client struct {
	UAID     string              `json:"uaid"`
	Channels map[string]*channel `json:"channels"`
	Queue    []*stored           `json:"queue"`
	LastSeen time.Time           `json:"lastSeen"`
	sess     *session
}

type chanRef struct {
	uaid, chid string
}

// Server is a relay.
type Server struct {
	cfg    Config
	secret []byte

	mu      sync.Mutex
	clients map[string]*client
	byEID   map[string]chanRef
	bytes   int
	dirty   bool
	closed  bool
	quit    chan struct{}
	wg      sync.WaitGroup

	ipLim, epLim *limiter
	wsLim        *limiter
	stats        Stats
}

// Stats are counters for /health and tests.
type Stats struct {
	Clients, Connected, Channels, Stored, StoredBytes int
	Accepted, Delivered, Expired, Rejected            int64
}

// New starts a relay.
func New(cfg Config) (*Server, error) {
	cfg.defaults()
	s := &Server{cfg: cfg, clients: map[string]*client{}, byEID: map[string]chanRef{}, quit: make(chan struct{})}
	s.ipLim = newLimiter(cfg.IPRate, cfg.IPBurst, cfg.Now)
	s.epLim = newLimiter(cfg.EndpointRate, cfg.EndpointBurst, cfg.Now)
	s.wsLim = newLimiter(2, 20, cfg.Now)
	s.secret = cfg.Secret
	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			return nil, err
		}
		if err := s.load(); err != nil {
			return nil, err
		}
	}
	if len(s.secret) == 0 {
		s.secret = make([]byte, 32)
		if _, err := rand.Read(s.secret); err != nil {
			return nil, err
		}
		if cfg.DataDir != "" {
			if err := os.WriteFile(filepath.Join(cfg.DataDir, "secret"), []byte(hex.EncodeToString(s.secret)), 0o600); err != nil {
				return nil, err
			}
		}
	}
	if len(s.secret) < 16 {
		return nil, errors.New("pushd: the secret must be at least 16 bytes")
	}
	s.wg.Add(1)
	go s.janitor()
	return s, nil
}

// Close stops the relay, closes every WebSocket and saves the state.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.quit)
	var ss []*session
	for _, c := range s.clients {
		if c.sess != nil {
			ss = append(ss, c.sess)
		}
	}
	s.mu.Unlock()
	for _, x := range ss {
		x.close()
	}
	s.wg.Wait()
	return s.save()
}

// Stats returns a snapshot of the counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.Clients = len(s.clients)
	for _, c := range s.clients {
		if c.sess != nil {
			st.Connected++
		}
		st.Channels += len(c.Channels)
		st.Stored += len(c.Queue)
	}
	st.StoredBytes = s.bytes
	return st
}

// Sweep drops expired messages and forgotten clients now (the janitor calls it every minute).
func (s *Server) Sweep() {
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for uaid, c := range s.clients {
		before := len(c.Queue)
		c.Queue = s.pruneLocked(c.Queue, now)
		if len(c.Queue) != before {
			s.dirty = true
		}
		if c.sess == nil && now.Sub(c.LastSeen) > s.cfg.ClientExpiry {
			s.dropClientLocked(uaid)
		}
	}
}

func (s *Server) pruneLocked(q []*stored, now time.Time) []*stored {
	out := q[:0]
	for _, m := range q {
		if now.Before(m.Expires) {
			out = append(out, m)
		} else {
			s.bytes -= len(m.Data)
			s.stats.Expired++
		}
	}
	return out
}

func (s *Server) dropClientLocked(uaid string) {
	c := s.clients[uaid]
	if c == nil {
		return
	}
	for _, ch := range c.Channels {
		delete(s.byEID, ch.EID)
	}
	for _, m := range c.Queue {
		s.bytes -= len(m.Data)
	}
	delete(s.clients, uaid)
	s.dirty = true
}

func (s *Server) janitor() {
	defer s.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
			n++
			if n%60 == 0 {
				s.Sweep()
			}
			_ = s.save()
		}
	}
}

// ---- persistence ------------------------------------------------------------------------------------------------------

type diskState struct {
	V       int       `json:"v"`
	Clients []*client `json:"clients"`
}

func (s *Server) load() error {
	if b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "secret")); err == nil && len(s.secret) == 0 {
		if d, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil {
			s.secret = d
		}
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "state.json"))
	if err != nil {
		return nil
	}
	var d diskState
	if err := json.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("pushd: state.json: %w", err)
	}
	now := s.cfg.Now()
	for _, c := range d.Clients {
		if c.Channels == nil {
			c.Channels = map[string]*channel{}
		}
		c.Queue = s.pruneLocked(c.Queue, now)
		for _, m := range c.Queue {
			s.bytes += len(m.Data)
		}
		for _, ch := range c.Channels {
			s.byEID[ch.EID] = chanRef{c.UAID, ch.ID}
		}
		s.clients[c.UAID] = c
	}
	return nil
}

func (s *Server) save() error {
	if s.cfg.DataDir == "" {
		return nil
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	s.dirty = false
	d := diskState{V: 1}
	for _, c := range s.clients {
		d.Clients = append(d.Clients, c)
	}
	sort.Slice(d.Clients, func(i, j int) bool { return d.Clients[i].UAID < d.Clients[j].UAID })
	b, err := json.Marshal(d)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.cfg.DataDir, "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.cfg.DataDir, "state.json"))
}

// ---- endpoints ----------------------------------------------------------------------------------------------------------

func (s *Server) mac(parts ...string) []byte {
	h := hmac.New(sha256.New, s.secret)
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return h.Sum(nil)[:16]
}

// token builds "<eid><mac>" (base64url) for an endpoint id; the endpoint id is random and means nothing outside the relay.
func (s *Server) token(eid string) string {
	id, _ := hex.DecodeString(eid)
	return webpush.B64(append(id, s.mac("endpoint", eid)...))
}

// parseToken verifies the HMAC and returns the endpoint id.
func (s *Server) parseToken(tok string) (string, bool) {
	b, err := webpush.DecodeB64(tok)
	if err != nil || len(b) != 32 {
		return "", false
	}
	eid := hex.EncodeToString(b[:16])
	if !hmac.Equal(b[16:], s.mac("endpoint", eid)) {
		return "", false
	}
	return eid, true
}

func (s *Server) base(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || (s.cfg.TrustForwarded && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- HTTP ----------------------------------------------------------------------------------------------------------------

// Handler serves the application-server API, the WebSocket and /health.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/wpush/v2/m/", s.handleMessage)
	mux.HandleFunc("/wpush/v2/", s.handlePush)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Stats())
	})
	ws := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil }, // browsers send no Origin
		Handler:   s.serveWS,
	}
	wsHandler := func(w http.ResponseWriter, r *http.Request) {
		if !s.wsLim.allow(s.remote(r)) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many connections", http.StatusTooManyRequests)
			return
		}
		ws.ServeHTTP(w, r)
	}
	mux.HandleFunc("/ws", wsHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			wsHandler(w, r)
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

func (s *Server) remote(r *http.Request) string {
	if s.cfg.TrustForwarded {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			return strings.TrimSpace(strings.Split(f, ",")[0])
		}
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func apiError(w http.ResponseWriter, status, errno int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "errno": errno, "error": http.StatusText(status), "message": msg})
}

func validTopic(t string) bool {
	if len(t) == 0 || len(t) > 32 {
		return false
	}
	for _, c := range t {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		apiError(w, http.StatusMethodNotAllowed, 100, "push messages are sent with POST")
		return
	}
	if !s.ipLim.allow(s.remote(r)) {
		s.reject(w, 429, 201, "too many requests", "1")
		return
	}
	tok := strings.TrimPrefix(r.URL.Path, "/wpush/v2/")
	eid, ok := s.parseToken(tok)
	if !ok {
		s.reject(w, 404, 102, "invalid push endpoint", "")
		return
	}
	if !s.epLim.allow(eid) {
		s.reject(w, 429, 201, "too many requests for this subscription", "1")
		return
	}
	ttlH := r.Header.Get("TTL")
	if ttlH == "" {
		s.reject(w, 400, 111, "the TTL header is required", "")
		return
	}
	ttl, err := strconv.ParseInt(ttlH, 10, 64)
	if err != nil || ttl < 0 {
		s.reject(w, 400, 112, "the TTL header must be a non-negative integer", "")
		return
	}
	urgency := strings.ToLower(r.Header.Get("Urgency"))
	switch urgency {
	case "", "very-low", "low", "normal", "high":
	default:
		s.reject(w, 400, 113, "invalid Urgency", "")
		return
	}
	topic := r.Header.Get("Topic")
	if topic != "" && !validTopic(topic) {
		s.reject(w, 400, 113, "invalid Topic (at most 32 characters of the URL-safe base64 alphabet)", "")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(s.cfg.MaxBody)+1))
	if err != nil {
		s.reject(w, 400, 108, "cannot read the body", "")
		return
	}
	if len(body) > s.cfg.MaxBody {
		s.reject(w, 413, 104, "the payload is too large", "")
		return
	}
	if len(body) > 0 && !strings.EqualFold(r.Header.Get("Content-Encoding"), "aes128gcm") {
		s.reject(w, 400, 110, "the body must use Content-Encoding: aes128gcm (RFC 8291)", "")
		return
	}
	now := s.cfg.Now()
	keyed := false

	s.mu.Lock()
	ref, ok := s.byEID[eid]
	var c *client
	var ch *channel
	if ok {
		c = s.clients[ref.uaid]
		if c != nil {
			ch = c.Channels[ref.chid]
		}
	}
	if ch != nil {
		keyed = len(ch.Key) > 0
	}
	s.mu.Unlock()
	if ch == nil {
		s.reject(w, 410, 103, "this subscription has expired or was removed", "")
		return
	}
	if keyed || s.cfg.RequireVAPID || r.Header.Get("Authorization") != "" {
		claims, err := webpush.VerifyVAPID(r.Header.Get("Authorization"), r.Header.Get("Crypto-Key"), s.base(r), now)
		if err != nil {
			s.reject(w, 401, 109, err.Error(), "")
			return
		}
		if keyed && !hmac.Equal(claims.Key, ch.Key) {
			s.reject(w, 403, 109, "the VAPID key is not the one this subscription was made with", "")
			return
		}
	}

	ttlD := time.Duration(ttl) * time.Second
	if ttlD > s.cfg.MaxTTL {
		ttlD = s.cfg.MaxTTL
	}
	msgID := randHex(16)

	s.mu.Lock()
	// the subscription may have been removed meanwhile
	if c = s.clients[ref.uaid]; c == nil || c.Channels[ref.chid] == nil {
		s.mu.Unlock()
		s.reject(w, 410, 103, "this subscription has expired or was removed", "")
		return
	}
	s.stats.Accepted++
	m := &stored{ID: msgID, Channel: ref.chid, Data: body, Expires: now.Add(ttlD), Topic: topic, Urgency: urgency, At: now}
	deliverNow := c.sess != nil && c.sess.ready
	// TTL 0 means "now or never" (RFC 8030 section 5.2): it is delivered to a connected browser and never stored.
	if ttl > 0 {
		if s.bytes+len(body) > s.cfg.MaxStoredBytes {
			s.mu.Unlock()
			s.reject(w, 503, 202, "the relay's message store is full", "60")
			return
		}
		if topic != "" { // a message with the same topic replaces the one still waiting
			q := c.Queue[:0]
			for _, o := range c.Queue {
				if o.Topic == topic && o.Channel == ref.chid {
					s.bytes -= len(o.Data)
					continue
				}
				q = append(q, o)
			}
			c.Queue = q
		}
		c.Queue = append(c.Queue, m)
		s.bytes += len(body)
		for len(c.Queue) > s.cfg.MaxQueuePerClient {
			s.bytes -= len(c.Queue[0].Data)
			c.Queue = c.Queue[1:]
		}
		s.dirty = true
	}
	if deliverNow {
		c.sess.deliver(m)
		s.stats.Delivered++
	}
	s.mu.Unlock()

	w.Header().Set("Location", s.base(r)+"/wpush/v2/m/"+webpush.B64(append([]byte(msgID), s.mac("msg", msgID)...)))
	w.Header().Set("TTL", strconv.FormatInt(int64(ttlD/time.Second), 10))
	w.Header().Set("Date", now.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) reject(w http.ResponseWriter, status, errno int, msg, retry string) {
	s.mu.Lock()
	s.stats.Rejected++
	s.mu.Unlock()
	if retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	apiError(w, status, errno, msg)
}

// handleMessage serves DELETE on a message resource (RFC 8030 section 5.2): the application server withdraws a
// message that has not been delivered yet.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		apiError(w, http.StatusMethodNotAllowed, 100, "message resources only accept DELETE")
		return
	}
	b, err := webpush.DecodeB64(strings.TrimPrefix(r.URL.Path, "/wpush/v2/m/"))
	if err != nil || len(b) != 32+16 || !hmac.Equal(b[32:], s.mac("msg", string(b[:32]))) {
		apiError(w, http.StatusNotFound, 102, "invalid message resource")
		return
	}
	id := string(b[:32])
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.clients {
		for i, m := range c.Queue {
			if m.ID == id {
				s.bytes -= len(m.Data)
				c.Queue = append(c.Queue[:i], c.Queue[i+1:]...)
				s.dirty = true
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	apiError(w, http.StatusNotFound, 102, "the message was delivered or has expired")
}

// ---- WebSocket ----------------------------------------------------------------------------------------------------------

type session struct {
	s     *Server
	ws    *websocket.Conn
	out   chan []byte
	done  chan struct{}
	once  sync.Once
	uaid  string
	ready bool // hello completed; guarded by s.mu
}

func (x *session) close() {
	x.once.Do(func() {
		close(x.done)
		_ = x.ws.Close()
	})
}

// send queues a frame for the writer goroutine; a client that cannot keep up is disconnected (its messages stay stored).
func (x *session) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case x.out <- b:
	default:
		go x.close()
	}
}

// deliver sends a stored message (s.mu held by the caller).
func (x *session) deliver(m *stored) {
	n := map[string]any{"messageType": "notification", "channelID": m.Channel, "version": m.ID,
		"ttl": int(time.Until(m.Expires).Seconds()), "timestamp": m.At.Unix()}
	if len(m.Data) > 0 {
		n["data"] = webpush.B64(m.Data)
		n["headers"] = map[string]string{"encoding": "aes128gcm"}
	}
	x.send(n)
}

type wsIn struct {
	MessageType string         `json:"messageType"`
	UAID        string         `json:"uaid"`
	ChannelIDs  []string       `json:"channelIDs"`
	ChannelID   string         `json:"channelID"`
	Key         string         `json:"key"`
	Updates     []wsAckUpdate  `json:"updates"`
	Broadcasts  map[string]any `json:"broadcasts"`
}

type wsAckUpdate struct {
	ChannelID string `json:"channelID"`
	Version   string `json:"version"`
	Code      int    `json:"code"`
}

func validChannelID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Server) serveWS(ws *websocket.Conn) {
	x := &session{s: s, ws: ws, out: make(chan []byte, 256), done: make(chan struct{})}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case b := <-x.out:
				_ = ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if websocket.Message.Send(ws, string(b)) != nil {
					x.close()
					return
				}
			case <-x.done:
				return
			}
		}
	}()
	defer func() {
		x.close()
		s.mu.Lock()
		if c := s.clients[x.uaid]; c != nil && c.sess == x {
			c.sess = nil
			c.LastSeen = s.cfg.Now()
			s.dirty = true
		}
		s.mu.Unlock()
	}()
	for {
		_ = ws.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
		var raw string
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			return
		}
		if len(raw) > 64<<10 {
			return
		}
		var in wsIn
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			return
		}
		if in.MessageType == "" { // "{}" is the ping
			x.send(map[string]any{})
			continue
		}
		s.mu.Lock()
		ready := x.ready
		s.mu.Unlock()
		if in.MessageType != "hello" && !ready {
			return // the protocol starts with hello
		}
		switch in.MessageType {
		case "hello":
			if ready {
				return
			}
			s.hello(x, &in)
		case "register":
			s.register(x, &in)
		case "unregister":
			s.unregister(x, &in)
		case "ack":
			s.ack(x, &in)
		case "nack", "broadcast_subscribe":
		default:
		}
	}
}

func (s *Server) hello(x *session, in *wsIn) {
	now := s.cfg.Now()
	s.mu.Lock()
	c := s.clients[in.UAID]
	if c == nil || in.UAID == "" {
		c = &client{UAID: randHex(16), Channels: map[string]*channel{}}
		s.clients[c.UAID] = c
	}
	if c.sess != nil && c.sess != x {
		old := c.sess
		c.sess = nil
		go old.close() // one connection per browser: the newest wins
	}
	c.LastSeen = now
	c.sess = x
	x.uaid = c.UAID
	x.ready = true
	s.dirty = true
	x.send(map[string]any{"messageType": "hello", "uaid": c.UAID, "status": 200, "use_webpush": true, "broadcasts": map[string]any{}})
	c.Queue = s.pruneLocked(c.Queue, now)
	for _, m := range c.Queue {
		x.deliver(m)
		s.stats.Delivered++
	}
	s.mu.Unlock()
}

func (s *Server) register(x *session, in *wsIn) {
	reply := func(status int, extra map[string]any) {
		m := map[string]any{"messageType": "register", "channelID": in.ChannelID, "status": status}
		for k, v := range extra {
			m[k] = v
		}
		x.send(m)
	}
	if !validChannelID(in.ChannelID) {
		reply(401, map[string]any{"error": "invalid channelID"})
		return
	}
	var key []byte
	if in.Key != "" {
		b, err := webpush.DecodeB64(in.Key)
		if err == nil {
			_, err = webpush.ParsePublicKey(b)
		}
		if err != nil {
			reply(400, map[string]any{"error": "invalid key"})
			return
		}
		key = b
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[x.uaid]
	if c == nil {
		reply(401, map[string]any{"error": "unknown client"})
		return
	}
	if _, dup := c.Channels[in.ChannelID]; dup {
		reply(409, map[string]any{"error": "the channel is already registered"})
		return
	}
	if len(c.Channels) >= s.cfg.MaxChannels {
		reply(500, map[string]any{"error": "too many subscriptions"})
		return
	}
	ch := &channel{ID: in.ChannelID, EID: randHex(16), Key: key, Created: s.cfg.Now()}
	c.Channels[ch.ID] = ch
	s.byEID[ch.EID] = chanRef{c.UAID, ch.ID}
	s.dirty = true
	reply(200, map[string]any{"pushEndpoint": s.endpointURL(x, ch)})
}

func (s *Server) endpointURL(x *session, ch *channel) string {
	base := s.cfg.PublicURL
	if base == "" {
		scheme := "http"
		if req := x.ws.Request(); req != nil {
			if req.TLS != nil || (s.cfg.TrustForwarded && req.Header.Get("X-Forwarded-Proto") == "https") {
				scheme = "https"
			}
			base = scheme + "://" + req.Host
		}
	}
	return strings.TrimRight(base, "/") + "/wpush/v2/" + s.token(ch.EID)
}

func (s *Server) unregister(x *session, in *wsIn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[x.uaid]
	status := 200
	if c != nil {
		if ch := c.Channels[in.ChannelID]; ch != nil {
			delete(s.byEID, ch.EID)
			delete(c.Channels, in.ChannelID)
			q := c.Queue[:0]
			for _, m := range c.Queue {
				if m.Channel == in.ChannelID {
					s.bytes -= len(m.Data)
					continue
				}
				q = append(q, m)
			}
			c.Queue = q
			s.dirty = true
		}
	}
	x.send(map[string]any{"messageType": "unregister", "channelID": in.ChannelID, "status": status})
}

func (s *Server) ack(x *session, in *wsIn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[x.uaid]
	if c == nil {
		return
	}
	for _, u := range in.Updates {
		for i, m := range c.Queue {
			if m.ID == u.Version && m.Channel == u.ChannelID {
				s.bytes -= len(m.Data)
				c.Queue = append(c.Queue[:i], c.Queue[i+1:]...)
				s.dirty = true
				break
			}
		}
	}
}

// Endpoint helpers for tests and tooling.

// EndpointValid parses a push endpoint and reports whether its token is valid for this relay.
func (s *Server) EndpointValid(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	_, ok := s.parseToken(strings.TrimPrefix(u.Path, "/wpush/v2/"))
	return ok
}
