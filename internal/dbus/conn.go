package dbus

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Message types and header fields (D-Bus specification, "Message Format").
const (
	TypeMethodCall = 1
	TypeReply      = 2
	TypeError      = 3
	TypeSignal     = 4

	flagNoReply = 1

	fieldPath        = 1
	fieldInterface   = 2
	fieldMember      = 3
	fieldErrorName   = 4
	fieldReplySerial = 5
	fieldDestination = 6
	fieldSender      = 7
	fieldSignature   = 8
)

const maxMessage = 4 << 20

// Message is a decoded D-Bus message.
type Message struct {
	Type        int
	Serial      uint32
	ReplySerial uint32
	Path        string
	Interface   string
	Member      string
	ErrorName   string
	Destination string
	Sender      string
	Signature   string
	Body        []any
}

// Error is a D-Bus error reply.
type Error struct {
	Name string
	Msg  string
}

func (e *Error) Error() string { return e.Name + ": " + e.Msg }

// Conn is a connection to a message bus (or a direct peer).
type Conn struct {
	rwc    io.ReadWriteCloser
	order  byteOrder
	wmu    sync.Mutex
	mu     sync.Mutex
	serial uint32
	calls  map[uint32]chan *Message
	sigFn  func(*Message)
	callFn func(*Message) (sig string, body []any, err *Error)
	unique string
	closed bool
	done   chan struct{}
}

// Option configures NewConn.
type Option func(*connConfig)

type connConfig struct {
	server bool
	hello  bool
}

// Peer makes a connection that does not talk to a bus daemon (no Hello): a direct connection to one service.
func Peer() Option { return func(c *connConfig) { c.hello = false } }

// ServerSide runs the server half of the authentication handshake (for tests and in-process fakes).
func ServerSide() Option { return func(c *connConfig) { c.server = true; c.hello = false } }

// SessionAddress returns the session bus address from the environment.
func SessionAddress() string {
	if a := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); a != "" {
		return a
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return "unix:path=" + x + "/bus"
	}
	return ""
}

// Dial connects to the bus at address (a ";"-free list is tried in order: "unix:path=...", "unix:abstract=...",
// optionally with ",guid=...") and says Hello.
func Dial(address string) (*Conn, error) {
	if address == "" {
		return nil, errors.New("dbus: no session bus address (DBUS_SESSION_BUS_ADDRESS is not set)")
	}
	var last error
	for _, a := range strings.Split(address, ";") {
		nc, err := dialOne(a)
		if err != nil {
			last = err
			continue
		}
		c, err := NewConn(nc)
		if err != nil {
			nc.Close()
			last = err
			continue
		}
		return c, nil
	}
	return nil, last
}

func dialOne(a string) (net.Conn, error) {
	transport, params, ok := strings.Cut(a, ":")
	if !ok || transport != "unix" {
		return nil, fmt.Errorf("dbus: unsupported transport in %q", a)
	}
	kv := map[string]string{}
	for _, p := range strings.Split(params, ",") {
		if k, v, ok := strings.Cut(p, "="); ok {
			kv[k] = unescape(v)
		}
	}
	switch {
	case kv["path"] != "":
		return net.Dial("unix", kv["path"])
	case kv["abstract"] != "":
		return net.Dial("unix", "@"+kv["abstract"])
	}
	return nil, fmt.Errorf("dbus: no path in %q", a)
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// NewConn authenticates over rwc and, unless Peer or ServerSide is given, says Hello to a bus daemon.
func NewConn(rwc io.ReadWriteCloser, opts ...Option) (*Conn, error) {
	cfg := connConfig{hello: true}
	for _, o := range opts {
		o(&cfg)
	}
	c := &Conn{rwc: rwc, order: binary.LittleEndian, calls: map[uint32]chan *Message{}, done: make(chan struct{})}
	br := bufio.NewReader(rwc)
	var err error
	if cfg.server {
		err = serverAuth(rwc, br)
	} else {
		err = clientAuth(rwc, br)
	}
	if err != nil {
		return nil, err
	}
	go c.readLoop(br)
	if cfg.hello {
		ctx, cancel := context.WithTimeout(context.Background(), 10e9)
		defer cancel()
		m, err := c.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "Hello", "")
		if err != nil {
			c.Close()
			return nil, err
		}
		if len(m.Body) == 1 {
			c.unique, _ = m.Body[0].(string)
		}
	}
	return c, nil
}

func clientAuth(w io.Writer, br *bufio.Reader) error {
	uid := hex.EncodeToString([]byte(strconv.Itoa(os.Getuid())))
	if _, err := io.WriteString(w, "\x00AUTH EXTERNAL "+uid+"\r\n"); err != nil {
		return err
	}
	line, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("dbus: authentication: %w", err)
	}
	if !strings.HasPrefix(line, "OK") {
		return fmt.Errorf("dbus: the bus refused EXTERNAL authentication: %q", strings.TrimSpace(line))
	}
	_, err = io.WriteString(w, "BEGIN\r\n")
	return err
}

func serverAuth(w io.Writer, br *bufio.Reader) error {
	if b, err := br.ReadByte(); err != nil || b != 0 {
		return errors.New("dbus: the peer did not start with a NUL byte")
	}
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "AUTH EXTERNAL") {
		return fmt.Errorf("dbus: unexpected authentication line %q", line)
	}
	if _, err := io.WriteString(w, "OK 0123456789abcdef0123456789abcdef\r\n"); err != nil {
		return err
	}
	line, err = br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "BEGIN") {
		return fmt.Errorf("dbus: expected BEGIN, got %q", line)
	}
	return nil
}

// UniqueName is the connection's name on the bus (":1.42"); empty for a peer connection.
func (c *Conn) UniqueName() string { return c.unique }

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Close ends the connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	err := c.rwc.Close()
	return err
}

// OnSignal sets the function that receives every signal (it must not block).
func (c *Conn) OnSignal(f func(*Message)) {
	c.mu.Lock()
	c.sigFn = f
	c.mu.Unlock()
}

// Serve sets the function that answers method calls addressed to this connection. A nil error and a signature
// give a reply; an *Error gives an error reply.
func (c *Conn) Serve(f func(*Message) (sig string, body []any, err *Error)) {
	c.mu.Lock()
	c.callFn = f
	c.mu.Unlock()
}

func (c *Conn) readLoop(br *bufio.Reader) {
	defer func() {
		c.mu.Lock()
		c.closed = true
		pending := c.calls
		c.calls = map[uint32]chan *Message{}
		c.mu.Unlock()
		for _, ch := range pending {
			close(ch)
		}
		close(c.done)
		c.rwc.Close()
	}()
	for {
		m, err := readMessage(br)
		if err != nil {
			return
		}
		switch m.Type {
		case TypeReply, TypeError:
			c.mu.Lock()
			ch := c.calls[m.ReplySerial]
			delete(c.calls, m.ReplySerial)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case TypeSignal:
			c.mu.Lock()
			f := c.sigFn
			c.mu.Unlock()
			if f != nil {
				f(m)
			}
		case TypeMethodCall:
			c.mu.Lock()
			f := c.callFn
			c.mu.Unlock()
			go c.answer(f, m)
		}
	}
}

func (c *Conn) answer(f func(*Message) (string, []any, *Error), m *Message) {
	if f == nil {
		c.reply(m, TypeError, "", nil, &Error{"org.freedesktop.DBus.Error.UnknownMethod", "no such method"})
		return
	}
	sig, body, e := f(m)
	if e != nil {
		c.reply(m, TypeError, "", nil, e)
		return
	}
	c.reply(m, TypeReply, sig, body, nil)
}

func (c *Conn) reply(to *Message, typ int, sig string, body []any, e *Error) {
	f := map[byte]Variant{fieldReplySerial: {"u", to.Serial}}
	if to.Sender != "" {
		f[fieldDestination] = Variant{"s", to.Sender}
	}
	if e != nil {
		f[fieldErrorName] = Variant{"s", e.Name}
		sig, body = "s", []any{e.Msg}
	}
	_, _ = c.send(typ, 1, f, sig, body)
}

func readMessage(r io.Reader) (*Message, error) {
	var fixed [16]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return nil, err
	}
	var order binary.ByteOrder
	switch fixed[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		return nil, errors.New("dbus: bad endianness byte")
	}
	if fixed[3] != 1 {
		return nil, errors.New("dbus: unsupported protocol version")
	}
	bodyLen := order.Uint32(fixed[4:8])
	serial := order.Uint32(fixed[8:12])
	fieldsLen := order.Uint32(fixed[12:16])
	if fieldsLen > maxMessage || bodyLen > maxMessage {
		return nil, errors.New("dbus: message too large")
	}
	pad := (8 - int(fieldsLen)%8) % 8
	rest := make([]byte, int(fieldsLen)+pad+int(bodyLen))
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}
	m := &Message{Type: int(fixed[1]), Serial: serial}
	// the header field array starts at offset 12 of the message (its length is at 12); decode from the length word
	hdr := append(append(make([]byte, 12), fixed[12:16]...), rest[:fieldsLen]...)
	d := &decoder{buf: hdr, order: order, pos: 12}
	v, err := d.decode("a(yv)")
	if err != nil {
		return nil, err
	}
	for _, f := range v.([]any) {
		st := f.(Struct)
		code, val := st[0].(byte), st[1].(Variant).Value
		switch code {
		case fieldPath:
			m.Path, _ = asString(val)
		case fieldInterface:
			m.Interface, _ = val.(string)
		case fieldMember:
			m.Member, _ = val.(string)
		case fieldErrorName:
			m.ErrorName, _ = val.(string)
		case fieldReplySerial:
			m.ReplySerial, _ = val.(uint32)
		case fieldDestination:
			m.Destination, _ = val.(string)
		case fieldSender:
			m.Sender, _ = val.(string)
		case fieldSignature:
			s, _ := val.(Signature)
			m.Signature = string(s)
		}
	}
	if bodyLen > 0 {
		body, err := Decode(order, m.Signature, rest[int(fieldsLen)+pad:])
		if err != nil {
			return nil, err
		}
		m.Body = body
	}
	return m, nil
}

func (c *Conn) nextSerial() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.serial++
	return c.serial
}

func (c *Conn) send(typ, flags int, fields map[byte]Variant, sig string, body []any) (uint32, error) {
	return c.sendSerial(c.nextSerial(), typ, flags, fields, sig, body)
}

func (c *Conn) sendSerial(serial uint32, typ, flags int, fields map[byte]Variant, sig string, body []any) (uint32, error) {
	bb, err := Encode(c.order, sig, body...)
	if err != nil {
		return 0, err
	}
	if sig != "" {
		fields[fieldSignature] = Variant{"g", Signature(sig)}
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, errors.New("dbus: connection closed")
	}
	var fl []any
	for _, code := range []byte{fieldPath, fieldInterface, fieldMember, fieldErrorName, fieldReplySerial, fieldDestination, fieldSender, fieldSignature} {
		if v, ok := fields[code]; ok {
			fl = append(fl, Struct{code, v})
		}
	}
	// the header field array is aligned relative to the start of the message, so encode it in place after the
	// 12 fixed bytes
	e := &encoder{order: c.order}
	e.buf = append(e.buf, 'l', byte(typ), byte(flags), 1)
	e.buf = c.order.AppendUint32(e.buf, uint32(len(bb)))
	e.buf = c.order.AppendUint32(e.buf, serial)
	if err := e.encode("a(yv)", fl); err != nil {
		return 0, err
	}
	hdr := e.buf
	for len(hdr)%8 != 0 {
		hdr = append(hdr, 0)
	}
	hdr = append(hdr, bb...)
	if len(hdr) > maxMessage {
		return 0, errors.New("dbus: message too large")
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.rwc.Write(hdr)
	return serial, err
}

// Call invokes a method and waits for the reply. dest may be "" on a peer connection.
func (c *Conn) Call(ctx context.Context, dest, path, iface, method, sig string, args ...any) (*Message, error) {
	fields := map[byte]Variant{fieldPath: {"o", ObjectPath(path)}, fieldMember: {"s", method}}
	if iface != "" {
		fields[fieldInterface] = Variant{"s", iface}
	}
	if dest != "" {
		fields[fieldDestination] = Variant{"s", dest}
	}
	ch := make(chan *Message, 1)
	serial := c.nextSerial()
	c.mu.Lock()
	c.calls[serial] = ch // registered before sending: the reply can arrive before the write returns
	c.mu.Unlock()
	if _, err := c.sendSerial(serial, TypeMethodCall, 0, fields, sig, args); err != nil {
		c.mu.Lock()
		delete(c.calls, serial)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, errors.New("dbus: connection closed")
		}
		if m.Type == TypeError {
			msg := ""
			if len(m.Body) > 0 {
				msg, _ = m.Body[0].(string)
			}
			return nil, &Error{m.ErrorName, msg}
		}
		return m, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.calls, serial)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Signal sends a signal (to dest when non-empty, else broadcast).
func (c *Conn) Signal(dest, path, iface, member, sig string, args ...any) error {
	fields := map[byte]Variant{fieldPath: {"o", ObjectPath(path)}, fieldInterface: {"s", iface}, fieldMember: {"s", member}}
	if dest != "" {
		fields[fieldDestination] = Variant{"s", dest}
	}
	_, err := c.send(TypeSignal, flagNoReply, fields, sig, args)
	return err
}

// AddMatch asks the bus to route signals matching rule to this connection.
func (c *Conn) AddMatch(ctx context.Context, rule string) error {
	_, err := c.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "AddMatch", "s", rule)
	return err
}
