// Package autopush is a client of the open autopush WebSocket protocol (the one Mozilla's push service speaks),
// implementing webpush.Transport. The protocol is a stream of JSON text frames:
//
//	client: {"messageType":"hello","uaid":"<id or absent>","channelIDs":[...],"use_webpush":true}
//	server: {"messageType":"hello","uaid":"<id>","status":200}
//	client: {"messageType":"register","channelID":"<uuid>","key":"<VAPID public key, base64url>"}
//	server: {"messageType":"register","channelID":"...","status":200,"pushEndpoint":"https://..."}
//	client: {"messageType":"unregister","channelID":"..."}            server: {...,"status":200}
//	server: {"messageType":"notification","channelID":"...","version":"<id>","ttl":N,"data":"<base64url>"}
//	client: {"messageType":"ack","updates":[{"channelID":"...","version":"<id>","code":100}]}
//	either: {}    (ping, answered with {})
//
// The connection is kept for as long as the client lives: after a failure it reconnects with exponential
// backoff and presents its identity (uaid) and its channel ids again, so messages stored by the service in the
// meantime are delivered. There is no built-in service address: the caller supplies the URL.
package autopush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/webpush"
	"golang.org/x/net/websocket"
)

// Config configures a Client.
type Config struct {
	// URL is the service's WebSocket address (ws:// or wss://). Required.
	URL string
	// UAID is the identity stored from an earlier session ("" = a new one is issued).
	UAID    string
	Handler webpush.Handler

	PingInterval time.Duration // default 4 minutes; the connection is dropped when the service is silent for twice that
	MinBackoff   time.Duration // default 1 second
	MaxBackoff   time.Duration // default 5 minutes
	// After is the timer (default time.After); tests inject their own to make backoff and pings deterministic.
	After func(time.Duration) <-chan time.Time
	// Dial opens the WebSocket (default websocket.Dial with a 15 second handshake limit).
	Dial func(ctx context.Context, rawurl string) (*websocket.Conn, error)
	Logf func(format string, args ...any)
}

// Client implements webpush.Transport.
type Client struct {
	cfg Config

	mu      sync.Mutex
	conn    *websocket.Conn
	ready   chan struct{} // closed while connected
	up      bool
	uaid    string
	pending map[string]chan frame // register/unregister replies by "<type>:<channel>"
	wmu     sync.Mutex
	quit    chan struct{}
	done    chan struct{}
	attempt int // consecutive failed connections, for tests and diagnostics
}

type frame struct {
	MessageType  string `json:"messageType"`
	UAID         string `json:"uaid"`
	Status       int    `json:"status"`
	ChannelID    string `json:"channelID"`
	PushEndpoint string `json:"pushEndpoint"`
	Version      string `json:"version"`
	TTL          int    `json:"ttl"`
	Data         string `json:"data"`
	Error        string `json:"error"`
}

var _ webpush.Transport = (*Client)(nil)

// New starts connecting in the background.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return nil, fmt.Errorf("autopush: %q is not a ws:// or wss:// URL", cfg.URL)
	}
	if cfg.Handler == nil {
		return nil, errors.New("autopush: a Handler is required")
	}
	if cfg.PingInterval == 0 {
		cfg.PingInterval = 4 * time.Minute
	}
	if cfg.MinBackoff == 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	if cfg.Dial == nil {
		cfg.Dial = defaultDial
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	c := &Client{cfg: cfg, uaid: cfg.UAID, ready: make(chan struct{}), pending: map[string]chan frame{}, quit: make(chan struct{}), done: make(chan struct{})}
	go c.run()
	return c, nil
}

func defaultDial(ctx context.Context, rawurl string) (*websocket.Conn, error) {
	wc, err := websocket.NewConfig(rawurl, "http://localhost/")
	if err != nil {
		return nil, err
	}
	type res struct {
		c   *websocket.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := websocket.DialConfig(wc)
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// Close stops the client.
func (c *Client) Close() error {
	select {
	case <-c.quit:
		return nil
	default:
	}
	close(c.quit)
	c.mu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.mu.Unlock()
	<-c.done
	return nil
}

// Connected reports whether the hello handshake has completed on the current connection.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.up
}

// UAID is the identity the service assigned.
func (c *Client) UAID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.uaid
}

func (c *Client) run() {
	defer close(c.done)
	backoff := c.cfg.MinBackoff
	for {
		select {
		case <-c.quit:
			return
		default:
		}
		ok := c.session()
		select {
		case <-c.quit:
			return
		default:
		}
		if ok {
			backoff = c.cfg.MinBackoff // it worked for a while: start over
		}
		select {
		case <-c.quit:
			return
		case <-c.cfg.After(backoff):
		}
		if !ok {
			if backoff *= 2; backoff > c.cfg.MaxBackoff {
				backoff = c.cfg.MaxBackoff
			}
		}
	}
}

func (c *Client) failPending() {
	c.mu.Lock()
	p := c.pending
	c.pending = map[string]chan frame{}
	c.mu.Unlock()
	for _, ch := range p {
		close(ch)
	}
}

// session runs one connection; it reports whether the hello handshake succeeded.
func (c *Client) session() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		select {
		case <-c.quit:
			cancel()
		case <-ctx.Done():
		}
	}()
	conn, err := c.cfg.Dial(ctx, c.cfg.URL)
	cancel()
	if err != nil {
		c.cfg.Logf("autopush: dial: %v", err)
		return false
	}
	defer conn.Close()
	c.mu.Lock()
	c.conn = conn
	uaid := c.uaid
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	hello := map[string]any{"messageType": "hello", "use_webpush": true, "channelIDs": c.cfg.Handler.Channels()}
	if uaid != "" {
		hello["uaid"] = uaid
	}
	if err := c.write(conn, hello); err != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	var f frame
	if err := c.read(conn, &f); err != nil || f.MessageType != "hello" || f.Status != 200 || f.UAID == "" {
		c.cfg.Logf("autopush: hello failed: %v %+v", err, f)
		return false
	}
	c.mu.Lock()
	changed := f.UAID != c.uaid
	hadOld := c.uaid != ""
	c.uaid = f.UAID
	c.up = true
	close(c.ready)
	c.mu.Unlock()
	if changed {
		c.cfg.Handler.OnIdentity(f.UAID)
		if hadOld {
			c.cfg.Handler.OnReset(f.UAID)
		}
	}
	defer func() {
		c.mu.Lock()
		c.up = false
		c.ready = make(chan struct{})
		c.mu.Unlock()
		c.failPending()
	}()

	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		for {
			select {
			case <-stopPing:
				return
			case <-c.quit:
				return
			case <-c.cfg.After(c.cfg.PingInterval):
				if c.write(conn, map[string]any{}) != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(2*c.cfg.PingInterval + 30*time.Second))
		var m frame
		if err := c.read(conn, &m); err != nil {
			return true
		}
		switch m.MessageType {
		case "":
			// ping reply
		case "register", "unregister":
			c.mu.Lock()
			ch := c.pending[m.MessageType+":"+m.ChannelID]
			delete(c.pending, m.MessageType+":"+m.ChannelID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case "notification":
			msg := webpush.Message{ChannelID: m.ChannelID, ID: m.Version, TTL: m.TTL}
			if m.Data != "" {
				b, err := webpush.DecodeB64(m.Data)
				if err != nil {
					_ = c.write(conn, ackFrame(m.ChannelID, m.Version, webpush.AckDecryptError))
					continue
				}
				msg.Data = b
			}
			c.cfg.Handler.OnMessage(msg)
		}
	}
}

func (c *Client) read(conn *websocket.Conn, f *frame) error {
	var raw string
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		return err
	}
	*f = frame{}
	return json.Unmarshal([]byte(raw), f)
}

func (c *Client) write(conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
	return websocket.Message.Send(conn, string(b))
}

func ackFrame(chid, version string, code webpush.AckCode) map[string]any {
	return map[string]any{"messageType": "ack", "updates": []map[string]any{{"channelID": chid, "version": version, "code": int(code)}}}
}

// waitUp blocks until the connection is established.
func (c *Client) waitUp(ctx context.Context) (*websocket.Conn, error) {
	for {
		c.mu.Lock()
		up, ready, conn := c.up, c.ready, c.conn
		c.mu.Unlock()
		if up && conn != nil {
			return conn, nil
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", webpush.ErrUnavailable, ctx.Err())
		case <-c.quit:
			return nil, webpush.ErrUnavailable
		}
	}
}

// request sends a register or unregister frame and waits for the answer.
func (c *Client) request(ctx context.Context, typ string, msg map[string]any, chid string) (frame, error) {
	conn, err := c.waitUp(ctx)
	if err != nil {
		return frame{}, err
	}
	key := typ + ":" + chid
	ch := make(chan frame, 1)
	c.mu.Lock()
	if _, busy := c.pending[key]; busy {
		c.mu.Unlock()
		return frame{}, errors.New("autopush: a request for this channel is already in flight")
	}
	c.pending[key] = ch
	c.mu.Unlock()
	if err := c.write(conn, msg); err != nil {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		return frame{}, fmt.Errorf("%w: %v", webpush.ErrUnavailable, err)
	}
	select {
	case f, ok := <-ch:
		if !ok {
			return frame{}, webpush.ErrUnavailable
		}
		return f, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		return frame{}, fmt.Errorf("%w: %v", webpush.ErrUnavailable, ctx.Err())
	}
}

// Register implements webpush.Transport.
func (c *Client) Register(ctx context.Context, channelID string, appServerKey []byte) (string, error) {
	m := map[string]any{"messageType": "register", "channelID": channelID}
	if len(appServerKey) > 0 {
		m["key"] = webpush.B64(appServerKey)
	}
	f, err := c.request(ctx, "register", m, channelID)
	if err != nil {
		return "", err
	}
	if f.Status != 200 || f.PushEndpoint == "" {
		return "", fmt.Errorf("autopush: register failed with status %d: %s", f.Status, f.Error)
	}
	return f.PushEndpoint, nil
}

// Unregister implements webpush.Transport.
func (c *Client) Unregister(ctx context.Context, channelID string) error {
	f, err := c.request(ctx, "unregister", map[string]any{"messageType": "unregister", "channelID": channelID}, channelID)
	if err != nil {
		return err
	}
	if f.Status != 200 {
		return fmt.Errorf("autopush: unregister failed with status %d", f.Status)
	}
	return nil
}

// Ack implements webpush.Transport. An acknowledgement made while disconnected is dropped: the service
// delivers the message again on the next connection.
func (c *Client) Ack(channelID, messageID string, code webpush.AckCode) {
	c.mu.Lock()
	conn := c.conn
	up := c.up
	c.mu.Unlock()
	if conn != nil && up {
		_ = c.write(conn, ackFrame(channelID, messageID, code))
	}
}
