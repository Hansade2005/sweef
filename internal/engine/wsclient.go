package engine

// wsclient.go: the WebSocket client of the engine (RFC 6455), written for what a browser has to get right and
// golang.org/x/net/websocket does not do: it delivers every frame separately instead of messages (fragmented
// messages arrive in pieces), it does not say whether a message was text or binary, it hides the close code and
// reason, and it reads the handshake response only to throw it away (no cookies, no subprotocol check). This
// client reassembles fragments, keeps the opcode, validates text as UTF-8 and control frames as the RFC says,
// reports the close code and reason with whether the closing handshake completed, and sends through a queue so
// that WebSocket.bufferedAmount is real and a slow server cannot block the page.
//
// Not supported: permessage-deflate (it is never offered, so servers do not use it).

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA

	// wsMaxMessage bounds one reassembled message (a hostile server could otherwise fill memory with fragments).
	wsMaxMessage = 128 << 20
	// wsCloseWait is how long a close handshake we started may take before the TCP connection is dropped.
	wsCloseWait = 5 * time.Second
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsClose describes how a connection ended.
type wsClose struct {
	Code   int    // 1005 when the peer's close frame had no status, 1006 when there was no close frame at all
	Reason string //
	Clean  bool   // the closing handshake completed
}

type wsOut struct {
	op      byte
	payload []byte
}

// wsClient is one open WebSocket.
type wsClient struct {
	conn  net.Conn
	br    *bufio.Reader
	proto string

	qmu      sync.Mutex
	qcond    *sync.Cond
	queue    []wsOut
	qclosed  bool
	buffered atomic.Int64 // payload bytes queued and not yet written (WebSocket.bufferedAmount)

	closeSent atomic.Bool
	closeOnce sync.Once
	done      chan struct{}
	wdone     chan struct{} // the writer goroutine ended
}

// wsHandshake performs the opening handshake on conn. origin and header are what the page's request carries.
func wsHandshake(conn net.Conn, u *url.URL, origin string, protocols []string, header http.Header) (*wsClient, *http.Response, error) {
	keyRaw := make([]byte, 16)
	if _, err := rand.Read(keyRaw); err != nil {
		return nil, nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyRaw)
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	var b strings.Builder
	b.WriteString("GET " + path + " HTTP/1.1\r\n")
	b.WriteString("Host: " + u.Host + "\r\n")
	b.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n")
	b.WriteString("Sec-WebSocket-Key: " + key + "\r\n")
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	if origin != "" {
		b.WriteString("Origin: " + origin + "\r\n")
	}
	if len(protocols) > 0 {
		b.WriteString("Sec-WebSocket-Protocol: " + strings.Join(protocols, ", ") + "\r\n")
	}
	for k, vs := range header {
		switch http.CanonicalHeaderKey(k) {
		case "Host", "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Protocol", "Sec-Websocket-Extensions", "Origin":
			continue
		}
		for _, v := range vs {
			if strings.ContainsAny(v, "\r\n") || strings.ContainsAny(k, "\r\n:") {
				continue
			}
			b.WriteString(k + ": " + v + "\r\n")
		}
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return nil, nil, err
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		resp.Body.Close()
		return nil, resp, fmt.Errorf("Error during WebSocket handshake: Unexpected response code: %d", resp.StatusCode)
	}
	fail := func(m string) (*wsClient, *http.Response, error) {
		return nil, resp, errors.New("Error during WebSocket handshake: " + m)
	}
	if !strings.EqualFold(strings.TrimSpace(resp.Header.Get("Upgrade")), "websocket") {
		return fail("'Upgrade' header is missing")
	}
	if !headerHasToken(resp.Header, "Connection", "upgrade") {
		return fail("'Connection' header is missing")
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		return fail("Incorrect 'Sec-WebSocket-Accept' header value")
	}
	if resp.Header.Get("Sec-WebSocket-Extensions") != "" {
		return fail("Unsupported extension '" + resp.Header.Get("Sec-WebSocket-Extensions") + "'")
	}
	proto := resp.Header.Get("Sec-WebSocket-Protocol")
	if proto != "" {
		ok := false
		for _, p := range protocols {
			if p == proto {
				ok = true
			}
		}
		if !ok {
			return fail("Sent non-empty 'Sec-WebSocket-Protocol' header but no subprotocol was requested or it does not match")
		}
	} else if len(protocols) > 0 {
		// Chromium accepts a server that selects none
		proto = ""
	}
	c := &wsClient{conn: conn, br: br, proto: proto, done: make(chan struct{}), wdone: make(chan struct{})}
	c.qcond = sync.NewCond(&c.qmu)
	go c.writeLoop()
	return c, resp, nil
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// Protocol is the subprotocol the server chose ("" when none).
func (c *wsClient) Protocol() string { return c.proto }

// BufferedAmount is the number of payload bytes queued for sending and not yet written to the socket.
func (c *wsClient) BufferedAmount() int64 { return c.buffered.Load() }

// Send queues a text or binary message. It never blocks on the network.
func (c *wsClient) Send(payload []byte, isBinary bool) error {
	if c.closeSent.Load() {
		return errors.New("InvalidStateError: the connection is closing")
	}
	op := byte(wsOpText)
	if isBinary {
		op = wsOpBinary
	}
	c.buffered.Add(int64(len(payload)))
	if !c.enqueue(wsOut{op: op, payload: payload}) {
		c.buffered.Add(-int64(len(payload)))
		return errors.New("InvalidStateError: the connection is closed")
	}
	return nil
}

func (c *wsClient) enqueue(o wsOut) bool {
	c.qmu.Lock()
	defer c.qmu.Unlock()
	if c.qclosed {
		return false
	}
	c.queue = append(c.queue, o)
	c.qcond.Signal()
	return true
}

// Close starts the closing handshake with a status and reason (code 0: no status). The reader still has to see
// the server's close frame (or time out) before the connection ends.
func (c *wsClient) Close(code int, reason string) {
	if !c.closeSent.CompareAndSwap(false, true) {
		return
	}
	var p []byte
	if code != 0 {
		p = make([]byte, 2, 2+len(reason))
		binary.BigEndian.PutUint16(p, uint16(code))
		p = append(p, reason...)
	}
	c.enqueue(wsOut{op: wsOpClose, payload: p})
	time.AfterFunc(wsCloseWait, c.Abort)
}

// Abort drops the TCP connection at once.
func (c *wsClient) Abort() {
	c.closeOnce.Do(func() {
		c.qmu.Lock()
		c.qclosed = true
		c.qcond.Broadcast()
		c.qmu.Unlock()
		c.conn.Close()
		close(c.done)
	})
}

func (c *wsClient) writeLoop() {
	defer close(c.wdone)
	bw := bufio.NewWriterSize(c.conn, 32<<10)
	for {
		c.qmu.Lock()
		for len(c.queue) == 0 && !c.qclosed {
			c.qcond.Wait()
		}
		if len(c.queue) == 0 && c.qclosed {
			c.qmu.Unlock()
			return
		}
		o := c.queue[0]
		c.queue[0] = wsOut{}
		c.queue = c.queue[1:]
		more := len(c.queue) > 0
		c.qmu.Unlock()
		err := wsWriteFrame(bw, o.op, o.payload)
		if err == nil && !more {
			err = bw.Flush()
		}
		if o.op == wsOpText || o.op == wsOpBinary {
			c.buffered.Add(-int64(len(o.payload)))
		}
		if err != nil {
			c.Abort()
			return
		}
	}
}

// wsWriteFrame writes one masked, unfragmented frame.
func wsWriteFrame(w *bufio.Writer, op byte, payload []byte) error {
	var hdr [14]byte
	hdr[0] = 0x80 | op
	n := len(payload)
	i := 2
	switch {
	case n < 126:
		hdr[1] = 0x80 | byte(n)
	case n <= 0xFFFF:
		hdr[1] = 0x80 | 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
		i = 4
	default:
		hdr[1] = 0x80 | 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
		i = 10
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	copy(hdr[i:], mask[:])
	if _, err := w.Write(hdr[:i+4]); err != nil {
		return err
	}
	// mask in chunks so a large message is not copied whole
	var buf [8192]byte
	for off := 0; off < n; {
		m := copy(buf[:], payload[off:])
		for k := 0; k < m; k++ {
			buf[k] ^= mask[(off+k)&3]
		}
		if _, err := w.Write(buf[:m]); err != nil {
			return err
		}
		off += m
	}
	return nil
}

type wsProtoError struct {
	code int
	msg  string
}

func (e *wsProtoError) Error() string { return e.msg }

// Read returns the next complete message. At the end it returns a *wsClose through err's chain: the caller reads
// the outcome with Outcome(err).
func (c *wsClient) Read() (payload []byte, isBinary bool, err error) {
	var msg []byte
	var msgOp byte
	inMsg := false
	for {
		var hdr [2]byte
		if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
			return nil, false, c.lost(err)
		}
		fin := hdr[0]&0x80 != 0
		rsv := hdr[0] & 0x70
		op := hdr[0] & 0x0F
		masked := hdr[1]&0x80 != 0
		n := uint64(hdr[1] & 0x7F)
		if rsv != 0 {
			return nil, false, c.fail(1002, "One or more reserved bits are on: reserved1 = 1, reserved2 = 0, reserved3 = 0")
		}
		if masked {
			return nil, false, c.fail(1002, "A server must not mask any frames that it sends to the client.")
		}
		switch n {
		case 126:
			var e [2]byte
			if _, err := io.ReadFull(c.br, e[:]); err != nil {
				return nil, false, c.lost(err)
			}
			n = uint64(binary.BigEndian.Uint16(e[:]))
		case 127:
			var e [8]byte
			if _, err := io.ReadFull(c.br, e[:]); err != nil {
				return nil, false, c.lost(err)
			}
			n = binary.BigEndian.Uint64(e[:])
			if n>>63 != 0 {
				return nil, false, c.fail(1002, "Invalid payload length")
			}
		}
		isControl := op&0x8 != 0
		if isControl && (n > 125 || !fin) {
			return nil, false, c.fail(1002, "Invalid control frame")
		}
		if !isControl && uint64(len(msg))+n > wsMaxMessage {
			return nil, false, c.fail(1009, "Message too big")
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(c.br, data); err != nil {
			return nil, false, c.lost(err)
		}
		switch op {
		case wsOpPing:
			c.enqueue(wsOut{op: wsOpPong, payload: data})
		case wsOpPong:
		case wsOpClose:
			code, reason := 1005, ""
			if len(data) == 1 {
				return nil, false, c.fail(1002, "Invalid close frame payload")
			}
			if len(data) >= 2 {
				code = int(binary.BigEndian.Uint16(data))
				reason = string(data[2:])
				if !wsValidCloseCode(code) || !utf8.ValidString(reason) {
					return nil, false, c.fail(1002, "Invalid close code or reason")
				}
			}
			if c.closeSent.CompareAndSwap(false, true) { // the server closed first: echo its status
				var p []byte
				if code != 1005 {
					p = make([]byte, 2)
					binary.BigEndian.PutUint16(p, uint16(code))
				}
				c.enqueue(wsOut{op: wsOpClose, payload: p})
			}
			c.finish()
			return nil, false, &wsClosed{wsClose{Code: code, Reason: reason, Clean: true}}
		case wsOpText, wsOpBinary:
			if inMsg {
				return nil, false, c.fail(1002, "Received start of new message but previous message is unfinished.")
			}
			inMsg, msgOp = true, op
			msg = append(msg[:0], data...)
			if fin {
				return c.deliver(msg, msgOp)
			}
		case wsOpContinuation:
			if !inMsg {
				return nil, false, c.fail(1002, "Received unexpected continuation frame.")
			}
			msg = append(msg, data...)
			if fin {
				return c.deliver(msg, msgOp)
			}
		default:
			return nil, false, c.fail(1002, fmt.Sprintf("Unrecognized frame opcode: %d", op))
		}
	}
}

func (c *wsClient) deliver(msg []byte, op byte) ([]byte, bool, error) {
	if op == wsOpText && !utf8.Valid(msg) {
		return nil, false, c.fail(1007, "Could not decode a text frame as UTF-8.")
	}
	out := make([]byte, len(msg))
	copy(out, msg)
	return out, op == wsOpBinary, nil
}

func wsValidCloseCode(c int) bool {
	switch {
	case c >= 3000 && c <= 4999:
		return true
	case c == 1000 || c == 1001 || c == 1002 || c == 1003 || (c >= 1007 && c <= 1011) || c == 1012 || c == 1013 || c == 1014:
		return true
	}
	return false
}

// wsClosed is the error Read returns when the connection ended.
type wsClosed struct{ wsClose }

func (e *wsClosed) Error() string {
	return fmt.Sprintf("websocket closed: %d %s", e.Code, e.Reason)
}

// finish ends the connection after the closing handshake: the writer flushes our close frame first.
func (c *wsClient) finish() {
	go func() {
		c.qmu.Lock()
		c.qclosed = true
		c.qcond.Broadcast()
		c.qmu.Unlock()
		select {
		case <-c.wdone:
		case <-time.After(2 * time.Second):
		}
		c.Abort()
	}()
}

// fail closes the connection because the peer broke the protocol.
func (c *wsClient) fail(code int, msg string) error {
	if c.closeSent.CompareAndSwap(false, true) {
		p := make([]byte, 2)
		binary.BigEndian.PutUint16(p, uint16(code))
		c.enqueue(wsOut{op: wsOpClose, payload: p})
	}
	c.finish()
	return &wsClosed{wsClose{Code: 1006, Reason: msg, Clean: false}}
}

// lost is the connection ending without a close frame.
func (c *wsClient) lost(err error) error {
	c.Abort()
	reason := ""
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		reason = err.Error()
	}
	return &wsClosed{wsClose{Code: 1006, Reason: reason, Clean: false}}
}

// wsOutcome extracts how a connection ended from the error Read returned.
func wsOutcome(err error) wsClose {
	var wc *wsClosed
	if errors.As(err, &wc) {
		return wc.wsClose
	}
	return wsClose{Code: 1006}
}
