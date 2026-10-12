//go:build linux

package x11

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file is a small hand-rolled X11 wire-protocol client: connection
// setup with MIT-MAGIC-COOKIE-1 auth, request framing, reply/error matching
// by sequence number, and an event reader goroutine. Only the requests the
// shell needs are implemented (see requests.go).

var le = binary.LittleEndian

// Error is an X protocol error.
type Error struct {
	Code     uint8
	Seq      uint16
	BadValue uint32
	Minor    uint16
	Major    uint8
}

func (e *Error) Error() string {
	return fmt.Sprintf("x11: protocol error code=%d major=%d minor=%d bad=%#x seq=%d",
		e.Code, e.Major, e.Minor, e.BadValue, e.Seq)
}

type result struct {
	buf []byte // full reply: 32-byte header + extra data
	err error
}

type call struct{ ch chan result }

// Wait returns the reply (32-byte header followed by extra data).
func (c *call) Wait() ([]byte, error) {
	r := <-c.ch
	return r.buf, r.err
}

// visualInfo is the subset of a visual we need.
type visualInfo struct {
	id                    uint32
	class                 uint8
	redMask, grnMask, blu uint32
}

type screenInfo struct {
	root       uint32
	colormap   uint32
	white      uint32
	black      uint32
	width      int
	height     int
	rootVisual uint32
	rootDepth  uint8
	visuals    map[uint32]visualInfo
}

type conn struct {
	nc net.Conn
	br *bufio.Reader

	wmu sync.Mutex
	seq uint16

	pmu     sync.Mutex
	pending map[uint16]*call

	// setup data
	releaseBase, idMask uint32
	idNext              uint32
	maxReqBytes         int
	imageByteOrder      uint8
	bitsPerPixel        map[uint8]uint8 // depth -> bpp
	scanlinePad         map[uint8]uint8
	minKeycode          uint8
	maxKeycode          uint8
	screen              screenInfo
	isUnix              bool

	extMu sync.Mutex
	exts  map[string]extInfo

	handler func(ev []byte) // called on the reader goroutine
	onError func(*Error)    // async (unmatched) protocol errors
	closeMu sync.Mutex
	closed  bool
	readErr error
	done    chan struct{}
}

type extInfo struct {
	present                     bool
	major, firstEvent, firstErr uint8
}

// dialDisplay connects and authenticates to the X server named by display
// ($DISPLAY syntax) and starts the reader goroutine.
func dialDisplay(display string) (*conn, error) {
	if display == "" {
		display = os.Getenv("DISPLAY")
	}
	if display == "" {
		return nil, errors.New("x11: DISPLAY is not set")
	}
	host, num, err := parseDisplay(display)
	if err != nil {
		return nil, err
	}
	var nc net.Conn
	isUnix := false
	switch {
	case host == "" || host == "unix" || strings.HasPrefix(host, "/"):
		path := "/tmp/.X11-unix/X" + strconv.Itoa(num)
		if strings.HasPrefix(host, "/") {
			path = host + ":" + strconv.Itoa(num)
		}
		nc, err = net.DialTimeout("unix", path, 3*time.Second)
		if err != nil {
			// abstract socket namespace, used by some servers
			nc, err = net.DialTimeout("unix", "@"+path, 3*time.Second)
		}
		isUnix = true
	default:
		nc, err = net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(6000+num)), 5*time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("x11: connect %q: %w", display, err)
	}
	c := &conn{
		nc:      nc,
		br:      bufio.NewReaderSize(nc, 1<<16),
		pending: map[uint16]*call{},
		exts:    map[string]extInfo{},
		isUnix:  isUnix,
		done:    make(chan struct{}),
	}
	name, data := readXauth(host, num)
	if err := c.handshake(name, data); err != nil {
		nc.Close()
		return nil, err
	}
	go c.readLoop()
	return c, nil
}

func parseDisplay(d string) (host string, num int, err error) {
	i := strings.LastIndexByte(d, ':')
	if i < 0 {
		return "", 0, fmt.Errorf("x11: bad DISPLAY %q", d)
	}
	host = d[:i]
	rest := d[i+1:]
	if j := strings.IndexByte(rest, '.'); j >= 0 {
		rest = rest[:j]
	}
	num, err = strconv.Atoi(rest)
	if err != nil {
		return "", 0, fmt.Errorf("x11: bad DISPLAY %q", d)
	}
	return host, num, nil
}

// readXauth finds a MIT-MAGIC-COOKIE-1 entry for this display.
func readXauth(host string, num int) (string, []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return "", nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	hostname, _ := os.Hostname()
	numStr := strconv.Itoa(num)
	type ent struct {
		family uint16
		addr   string
		number string
		name   string
		data   []byte
	}
	var best *ent
	r := b
	rd16 := func() (int, bool) {
		if len(r) < 2 {
			return 0, false
		}
		v := int(r[0])<<8 | int(r[1])
		r = r[2:]
		return v, true
	}
	rdStr := func() ([]byte, bool) {
		n, ok := rd16()
		if !ok || len(r) < n {
			return nil, false
		}
		s := r[:n]
		r = r[n:]
		return s, true
	}
	for len(r) > 0 {
		fam, ok := rd16()
		if !ok {
			break
		}
		addr, ok1 := rdStr()
		number, ok2 := rdStr()
		name, ok3 := rdStr()
		data, ok4 := rdStr()
		if !(ok1 && ok2 && ok3 && ok4) {
			break
		}
		e := ent{uint16(fam), string(addr), string(number), string(name), append([]byte(nil), data...)}
		if e.name != "MIT-MAGIC-COOKIE-1" {
			continue
		}
		if e.number != "" && e.number != numStr {
			continue
		}
		match := false
		switch {
		case e.family == 65535: // wild
			match = true
		case e.family == 256 && (host == "" || host == "unix"): // local
			match = e.addr == hostname || hostname == ""
		case host != "" && host != "unix":
			match = e.addr == host
		}
		if match {
			cp := e
			best = &cp
			if e.family != 65535 {
				break
			}
		}
	}
	if best == nil {
		return "", nil
	}
	return best.name, best.data
}

func pad4(n int) int { return (n + 3) &^ 3 }

func (c *conn) handshake(authName string, authData []byte) error {
	req := make([]byte, 12+pad4(len(authName))+pad4(len(authData)))
	req[0] = 'l'
	le.PutUint16(req[2:], 11)
	le.PutUint16(req[4:], 0)
	le.PutUint16(req[6:], uint16(len(authName)))
	le.PutUint16(req[8:], uint16(len(authData)))
	copy(req[12:], authName)
	copy(req[12+pad4(len(authName)):], authData)
	c.nc.SetDeadline(time.Now().Add(5 * time.Second))
	defer c.nc.SetDeadline(time.Time{})
	if _, err := c.nc.Write(req); err != nil {
		return err
	}
	var hdr [8]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return fmt.Errorf("x11: handshake: %w", err)
	}
	n := int(le.Uint16(hdr[6:])) * 4
	body := make([]byte, n)
	if _, err := io.ReadFull(c.br, body); err != nil {
		return fmt.Errorf("x11: handshake: %w", err)
	}
	if hdr[0] != 1 {
		reason := ""
		if rl := int(hdr[1]); rl <= len(body) {
			reason = string(body[:rl])
		}
		return fmt.Errorf("x11: server refused connection: %s", strings.TrimSpace(reason))
	}
	if len(body) < 32 {
		return errors.New("x11: short setup reply")
	}
	c.releaseBase = le.Uint32(body[4:])
	c.idMask = le.Uint32(body[8:])
	vendorLen := int(le.Uint16(body[16:]))
	c.maxReqBytes = int(le.Uint16(body[18:])) * 4
	nScreens := int(body[20])
	nFormats := int(body[21])
	c.imageByteOrder = body[22]
	c.minKeycode = body[26]
	c.maxKeycode = body[27]
	off := 32 + pad4(vendorLen)
	c.bitsPerPixel = map[uint8]uint8{}
	c.scanlinePad = map[uint8]uint8{}
	for i := 0; i < nFormats; i++ {
		if off+8 > len(body) {
			return errors.New("x11: truncated formats")
		}
		c.bitsPerPixel[body[off]] = body[off+1]
		c.scanlinePad[body[off]] = body[off+2]
		off += 8
	}
	if nScreens < 1 {
		return errors.New("x11: no screens")
	}
	if off+40 > len(body) {
		return errors.New("x11: truncated screen")
	}
	s := body[off:]
	c.screen = screenInfo{
		root:       le.Uint32(s[0:]),
		colormap:   le.Uint32(s[4:]),
		white:      le.Uint32(s[8:]),
		black:      le.Uint32(s[12:]),
		width:      int(le.Uint16(s[20:])),
		height:     int(le.Uint16(s[22:])),
		rootVisual: le.Uint32(s[32:]),
		rootDepth:  s[38],
		visuals:    map[uint32]visualInfo{},
	}
	nDepths := int(s[39])
	p := 40
	for d := 0; d < nDepths; d++ {
		if p+8 > len(s) {
			return errors.New("x11: truncated depth")
		}
		nv := int(le.Uint16(s[p+2:]))
		p += 8
		for v := 0; v < nv; v++ {
			if p+24 > len(s) {
				return errors.New("x11: truncated visual")
			}
			vi := visualInfo{
				id: le.Uint32(s[p:]), class: s[p+4],
				redMask: le.Uint32(s[p+8:]), grnMask: le.Uint32(s[p+12:]), blu: le.Uint32(s[p+16:]),
			}
			c.screen.visuals[vi.id] = vi
			p += 24
		}
	}
	if c.maxReqBytes < 4096 {
		c.maxReqBytes = 4096
	}
	return nil
}

// allocID returns a fresh resource ID.
func (c *conn) allocID() uint32 {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	// idMask has contiguous set bits; step by its lowest bit.
	inc := c.idMask & -c.idMask
	id := c.releaseBase | (c.idNext & c.idMask)
	c.idNext += inc
	return id
}

// send writes one request. buf must be a complete request whose length field
// (bytes 2..4) is already filled in. If reply is true the returned call
// yields the reply.
func (c *conn) send(buf []byte, reply bool) (*call, error) {
	var cl *call
	c.wmu.Lock()
	c.seq++
	seq := c.seq
	if reply {
		cl = &call{ch: make(chan result, 1)}
		c.pmu.Lock()
		c.pending[seq] = cl
		c.pmu.Unlock()
	}
	_, err := c.nc.Write(buf)
	c.wmu.Unlock()
	if err != nil {
		if reply {
			c.pmu.Lock()
			delete(c.pending, seq)
			c.pmu.Unlock()
		}
		return nil, err
	}
	return cl, nil
}

// do sends a request with a reply and waits for it.
func (c *conn) do(buf []byte) ([]byte, error) {
	cl, err := c.send(buf, true)
	if err != nil {
		return nil, err
	}
	select {
	case r := <-cl.ch:
		return r.buf, r.err
	case <-time.After(10 * time.Second):
		return nil, errors.New("x11: timeout waiting for reply")
	case <-c.done:
		return nil, c.err()
	}
}

func (c *conn) err() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.readErr != nil {
		return c.readErr
	}
	return errors.New("x11: connection closed")
}

// request builds a request header: opcode, data byte and total size in bytes
// (rounded up to 4).
func request(op, data uint8, size int) []byte {
	size = pad4(size)
	b := make([]byte, size)
	b[0] = op
	b[1] = data
	le.PutUint16(b[2:], uint16(size/4))
	return b
}

func (c *conn) readLoop() {
	var hdr [32]byte
	for {
		if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
			c.fail(err)
			return
		}
		switch t := hdr[0] & 0x7f; {
		case hdr[0] == 0: // error
			e := &Error{Code: hdr[1], Seq: le.Uint16(hdr[2:]), BadValue: le.Uint32(hdr[4:]),
				Minor: le.Uint16(hdr[8:]), Major: hdr[10]}
			c.pmu.Lock()
			cl := c.pending[e.Seq]
			delete(c.pending, e.Seq)
			c.pmu.Unlock()
			if cl != nil {
				cl.ch <- result{err: e}
			} else if c.onError != nil {
				c.onError(e)
			}
		case hdr[0] == 1: // reply
			extra := int(le.Uint32(hdr[4:])) * 4
			buf := make([]byte, 32+extra)
			copy(buf, hdr[:])
			if extra > 0 {
				if _, err := io.ReadFull(c.br, buf[32:]); err != nil {
					c.fail(err)
					return
				}
			}
			seq := le.Uint16(hdr[2:])
			c.pmu.Lock()
			cl := c.pending[seq]
			delete(c.pending, seq)
			c.pmu.Unlock()
			if cl != nil {
				cl.ch <- result{buf: buf}
			}
		default:
			ev := make([]byte, 32)
			copy(ev, hdr[:])
			if t == 35 { // GenericEvent carries extra words
				extra := int(le.Uint32(hdr[4:])) * 4
				if extra > 0 {
					more := make([]byte, extra)
					if _, err := io.ReadFull(c.br, more); err != nil {
						c.fail(err)
						return
					}
					ev = append(ev, more...)
				}
			}
			if c.handler != nil {
				c.handler(ev)
			}
		}
	}
}

func (c *conn) fail(err error) {
	c.closeMu.Lock()
	first := !c.closed
	c.closed = true
	if c.readErr == nil {
		c.readErr = err
	}
	c.closeMu.Unlock()
	if first {
		close(c.done)
	}
	// fail all pending calls
	c.pmu.Lock()
	for s, cl := range c.pending {
		cl.ch <- result{err: err}
		delete(c.pending, s)
	}
	c.pmu.Unlock()
}

// close shuts the socket; the reader goroutine then fails and closes done.
func (c *conn) close() { c.nc.Close() }

func (c *conn) isClosed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
