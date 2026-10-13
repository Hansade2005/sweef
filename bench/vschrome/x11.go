//go:build linux

package main

// x11.go: a minimal synchronous X11 client written for this harness (Xvfb with -ac, no auth). It is deliberately
// independent of internal/shell/x11 (whose conn type is unexported and tuned for the window system, not for
// measuring): the protocol framing follows the same rules (little endian, 32-byte replies/events, sequence matching)
// and the XTEST / GetImage usage mirrors what internal/shell/x11's tests do.
//
// Everything the benchmark does to the X server goes through this file, for both browsers alike:
//   - xtest:    fake key/button/motion input (XTEST FakeInput), timestamped in this process just before the write.
//   - GetImage: read pixels of the ROOT window (so popups, which are separate X windows, are included).
//   - DAMAGE:   be told when pixels of the root were drawn, without polling (no load on the server).

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

var le = binary.LittleEndian

const (
	opGetWindowAttrs = 3
	opGetGeometry    = 14
	opQueryTree      = 15
	opInternAtom     = 16
	opGetProperty    = 20
	opSetInputFocus  = 42
	opGetImage       = 73
	opQueryExtension = 98
	opGetKbdMapping  = 101
	opClearArea      = 61
	opPolyFillRect   = 70
	opCreateWindow   = 1
	opChangeWinAttrs = 2
	opMapWindow      = 8
	opCreateGC       = 55
	opChangeGC       = 56
	opConfigureWin   = 12
	opDestroyWindow  = 4
)

type xconn struct {
	nc     net.Conn
	br     *bufio.Reader
	seq    uint16
	root   uint32
	width  int
	height int
	base   uint32
	mask   uint32
	nextID uint32
	evq    [][]byte
	minKC  int
	maxKC  int
	// per-connection extension cache
	exts map[string]xext
}

type xext struct {
	present                  bool
	major, firstEv, firstErr uint8
}

type xevent struct {
	data []byte
	at   time.Time // when this process read it off the socket
}

func pad4(n int) int { return (n + 3) &^ 3 }

func dialX(display string) (*xconn, error) {
	num := strings.TrimPrefix(display, ":")
	if i := strings.IndexByte(num, '.'); i >= 0 {
		num = num[:i]
	}
	if _, err := strconv.Atoi(num); err != nil {
		return nil, fmt.Errorf("bad display %q", display)
	}
	nc, err := net.DialTimeout("unix", "/tmp/.X11-unix/X"+num, 3*time.Second)
	if err != nil {
		return nil, err
	}
	c := &xconn{nc: nc, br: bufio.NewReaderSize(nc, 1<<20), exts: map[string]xext{}}
	req := make([]byte, 12)
	req[0] = 'l'
	le.PutUint16(req[2:], 11)
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	defer nc.SetDeadline(time.Time{})
	if _, err := nc.Write(req); err != nil {
		nc.Close()
		return nil, err
	}
	var hdr [8]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		nc.Close()
		return nil, err
	}
	body := make([]byte, int(le.Uint16(hdr[6:]))*4)
	if _, err := io.ReadFull(c.br, body); err != nil {
		nc.Close()
		return nil, err
	}
	if hdr[0] != 1 {
		nc.Close()
		return nil, errors.New("X server refused the connection (is it started with -ac?)")
	}
	c.base = le.Uint32(body[4:])
	c.mask = le.Uint32(body[8:])
	vendor := int(le.Uint16(body[16:]))
	nScreens := int(body[20])
	nFormats := int(body[21])
	c.minKC, c.maxKC = int(body[26]), int(body[27])
	off := 32 + pad4(vendor) + 8*nFormats
	if nScreens < 1 || off+40 > len(body) {
		nc.Close()
		return nil, errors.New("short setup reply")
	}
	s := body[off:]
	c.root = le.Uint32(s[0:])
	c.width = int(le.Uint16(s[20:]))
	c.height = int(le.Uint16(s[22:]))
	return c, nil
}

func (c *xconn) close() { c.nc.Close() }

func (c *xconn) newID() uint32 {
	id := c.base | (c.nextID & c.mask)
	c.nextID++
	return id
}

func xreq(op, data uint8, size int) []byte {
	b := make([]byte, size)
	b[0] = op
	b[1] = data
	le.PutUint16(b[2:], uint16(size/4))
	return b
}

// send writes one request and returns its sequence number. The write is unbuffered: when send returns the request
// is in the kernel socket buffer of the X server.
func (c *xconn) send(b []byte) (uint16, error) {
	c.seq++
	_, err := c.nc.Write(b)
	return c.seq, err
}

// readMsg reads one 32-byte message plus the extra reply data, if any.
func (c *xconn) readMsg() ([]byte, error) {
	var h [32]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		return nil, err
	}
	if h[0] == 1 {
		n := int(le.Uint32(h[4:])) * 4
		if n > 0 {
			out := make([]byte, 32+n)
			copy(out, h[:])
			if _, err := io.ReadFull(c.br, out[32:]); err != nil {
				return nil, err
			}
			return out, nil
		}
	}
	return append([]byte(nil), h[:]...), nil
}

// reply waits for the reply (or error) of request seq, queueing any events that arrive meanwhile.
func (c *xconn) reply(seq uint16) ([]byte, error) {
	c.nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer c.nc.SetReadDeadline(time.Time{})
	for {
		m, err := c.readMsg()
		if err != nil {
			return nil, err
		}
		switch m[0] {
		case 1:
			if le.Uint16(m[2:]) == seq {
				return m, nil
			}
		case 0:
			if le.Uint16(m[2:]) == seq {
				return nil, fmt.Errorf("X error code %d (request major %d minor %d)", m[1], m[10], le.Uint16(m[8:]))
			}
		default:
			c.evq = append(c.evq, m)
		}
	}
}

func (c *xconn) call(b []byte) ([]byte, error) {
	seq, err := c.send(b)
	if err != nil {
		return nil, err
	}
	return c.reply(seq)
}

// nextEvent returns the next event, waiting until deadline (zero = forever). ok=false on timeout.
func (c *xconn) nextEvent(deadline time.Time) (xevent, bool, error) {
	if len(c.evq) > 0 {
		m := c.evq[0]
		c.evq = c.evq[1:]
		return xevent{m, time.Now()}, true, nil
	}
	for {
		if !deadline.IsZero() {
			c.nc.SetReadDeadline(deadline)
		} else {
			c.nc.SetReadDeadline(time.Time{})
		}
		m, err := c.readMsg()
		at := time.Now()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return xevent{}, false, nil
			}
			return xevent{}, false, err
		}
		if m[0] > 1 {
			return xevent{m, at}, true, nil
		}
	}
}

// pending reports whether events are already waiting (queued or readable without blocking).
func (c *xconn) drain() []xevent {
	var out []xevent
	for {
		ev, ok, err := c.nextEvent(time.Now().Add(time.Microsecond))
		if err != nil || !ok {
			return out
		}
		out = append(out, ev)
	}
}

func (c *xconn) sync() error {
	_, err := c.call(xreq(opGetInputFocusOp, 0, 4))
	return err
}

const opGetInputFocusOp = 43

func (c *xconn) extension(name string) (xext, error) {
	if e, ok := c.exts[name]; ok {
		return e, nil
	}
	b := xreq(opQueryExtension, 0, 8+pad4(len(name)))
	le.PutUint16(b[4:], uint16(len(name)))
	copy(b[8:], name)
	r, err := c.call(b)
	if err != nil {
		return xext{}, err
	}
	e := xext{present: r[8] != 0, major: r[9], firstEv: r[10], firstErr: r[11]}
	c.exts[name] = e
	if !e.present {
		return e, fmt.Errorf("X extension %s not present", name)
	}
	return e, nil
}

// ---- images ----------------------------------------------------------------------------------------------------

type rect struct{ x, y, w, h int }

func (r rect) intersects(o rect) bool {
	return r.x < o.x+o.w && o.x < r.x+r.w && r.y < o.y+o.h && o.y < r.y+r.h
}

// pixbuf is a ZPixmap grab, 4 bytes per pixel (B,G,R,X), rows packed.
type pixbuf struct {
	r   rect
	pix []byte
}

func (c *xconn) getImageReq(r rect) []byte {
	b := xreq(opGetImage, 2, 20)
	le.PutUint32(b[4:], c.root)
	le.PutUint16(b[8:], uint16(int16(r.x)))
	le.PutUint16(b[10:], uint16(int16(r.y)))
	le.PutUint16(b[12:], uint16(r.w))
	le.PutUint16(b[14:], uint16(r.h))
	le.PutUint32(b[16:], 0xffffffff)
	return b
}

func (c *xconn) grab(r rect) (*pixbuf, error) {
	rep, err := c.call(c.getImageReq(r))
	if err != nil {
		return nil, err
	}
	return &pixbuf{r: r, pix: rep[32:]}, nil
}

// grabMany pipelines several GetImage requests and then reads the replies (fewer round trips).
func (c *xconn) grabMany(rs []rect) ([]*pixbuf, error) {
	seqs := make([]uint16, len(rs))
	for i, r := range rs {
		s, err := c.send(c.getImageReq(r))
		if err != nil {
			return nil, err
		}
		seqs[i] = s
	}
	out := make([]*pixbuf, len(rs))
	for i, s := range seqs {
		rep, err := c.reply(s)
		if err != nil {
			return nil, err
		}
		out[i] = &pixbuf{r: rs[i], pix: rep[32:]}
	}
	return out, nil
}

func (a *pixbuf) equal(b *pixbuf) bool {
	if a == nil || b == nil || len(a.pix) != len(b.pix) {
		return false
	}
	for i := 0; i+3 < len(a.pix); i += 4 {
		// ignore the padding byte
		if a.pix[i] != b.pix[i] || a.pix[i+1] != b.pix[i+1] || a.pix[i+2] != b.pix[i+2] {
			return false
		}
	}
	return true
}

// diffPixels counts differing pixels.
func (a *pixbuf) diffPixels(b *pixbuf) int {
	if len(a.pix) != len(b.pix) {
		return -1
	}
	n := 0
	for i := 0; i+3 < len(a.pix); i += 4 {
		if a.pix[i] != b.pix[i] || a.pix[i+1] != b.pix[i+1] || a.pix[i+2] != b.pix[i+2] {
			n++
		}
	}
	return n
}

// nonModal counts pixels that differ from the most common of a small sample (cheap "is this blank?" test).
func (a *pixbuf) nonModal() int {
	if len(a.pix) < 4 {
		return 0
	}
	// the modal colour of a blank-ish UI is almost always the top-left or the colour of a coarse sample
	counts := map[uint32]int{}
	for i := 0; i+3 < len(a.pix); i += 4 * 61 {
		counts[le.Uint32(a.pix[i:])&0xffffff]++
	}
	var mode uint32
	best := -1
	for k, v := range counts {
		if v > best {
			best, mode = v, k
		}
	}
	n := 0
	for i := 0; i+3 < len(a.pix); i += 4 {
		if le.Uint32(a.pix[i:])&0xffffff != mode {
			n++
		}
	}
	return n
}

// ---- windows --------------------------------------------------------------------------------------------------

type xwin struct {
	id         uint32
	x, y, w, h int
	viewable   bool
	override   bool
}

func (c *xconn) children(parent uint32) ([]uint32, error) {
	b := xreq(opQueryTree, 0, 8)
	le.PutUint32(b[4:], parent)
	r, err := c.call(b)
	if err != nil {
		return nil, err
	}
	n := int(le.Uint16(r[16:]))
	out := make([]uint32, n)
	for i := range out {
		out[i] = le.Uint32(r[32+4*i:])
	}
	return out, nil
}

func (c *xconn) winInfo(id uint32) (xwin, error) {
	b := xreq(opGetWindowAttrs, 0, 8)
	le.PutUint32(b[4:], id)
	s1, _ := c.send(b)
	g := xreq(opGetGeometry, 0, 8)
	le.PutUint32(g[4:], id)
	s2, _ := c.send(g)
	ra, err1 := c.reply(s1)
	rg, err2 := c.reply(s2)
	if err1 != nil || err2 != nil {
		return xwin{}, errors.New("window gone")
	}
	return xwin{id: id, x: int(int16(le.Uint16(rg[12:]))), y: int(int16(le.Uint16(rg[14:]))), w: int(le.Uint16(rg[16:])), h: int(le.Uint16(rg[18:])),
		viewable: ra[26] == 2, override: ra[27] != 0}, nil
}

// topWindows lists the viewable children of the root.
func (c *xconn) topWindows() []xwin {
	ids, err := c.children(c.root)
	if err != nil {
		return nil
	}
	var out []xwin
	for _, id := range ids {
		if w, err := c.winInfo(id); err == nil && w.viewable {
			out = append(out, w)
		}
	}
	return out
}

func (c *xconn) setInputFocus(win uint32) error {
	b := xreq(opSetInputFocus, 1, 12) // revert-to PointerRoot
	le.PutUint32(b[4:], win)
	_, err := c.send(b)
	return err
}

// ---- XTEST -------------------------------------------------------------------------------------------------------

type xtest struct {
	c     *xconn
	major uint8
	km    map[rune]keyChord
}

type keyChord struct {
	code  uint8
	shift bool
}

func newXtest(c *xconn) (*xtest, error) {
	e, err := c.extension("XTEST")
	if err != nil {
		return nil, err
	}
	x := &xtest{c: c, major: e.major, km: map[rune]keyChord{}}
	if err := x.loadKeymap(); err != nil {
		return nil, err
	}
	return x, nil
}

// loadKeymap maps ASCII and the few named keys to keycodes using the server's keyboard mapping (level 0 and the
// shifted level 1).
func (x *xtest) loadKeymap() error {
	c := x.c
	n := c.maxKC - c.minKC + 1
	b := xreq(opGetKbdMapping, 0, 8)
	b[4] = byte(c.minKC)
	b[5] = byte(n)
	r, err := c.call(b)
	if err != nil {
		return err
	}
	per := int(r[1])
	sym := func(kc, level int) uint32 {
		if level >= per {
			return 0
		}
		return le.Uint32(r[32+((kc-c.minKC)*per+level)*4:])
	}
	for kc := c.minKC; kc <= c.maxKC; kc++ {
		for level := 0; level < 2; level++ {
			s := sym(kc, level)
			if s == 0 {
				continue
			}
			var ch rune
			switch {
			case s >= 0x20 && s < 0x7f:
				ch = rune(s)
			case s == 0xff0d:
				ch = '\n'
			case s == 0xff1b:
				ch = keyEsc
			case s == 0xff08:
				ch = keyBackspace
			case s == 0xff09:
				ch = '\t'
			case s == 0xffff:
				ch = keyDelete
			case s == 0xffe3 && level == 0:
				ch = keyCtrl
			case s == 0xffe1 && level == 0:
				ch = keyShift
			case s == 0xff51:
				ch = keyLeft
			case s == 0xff55 && level == 0:
				ch = keyPgUp
			case s == 0xff56 && level == 0:
				ch = keyPgDn
			default:
				continue
			}
			if _, dup := x.km[ch]; !dup {
				x.km[ch] = keyChord{uint8(kc), level == 1}
			}
		}
	}
	return nil
}

const (
	keyEsc rune = 0xe000 + iota
	keyBackspace
	keyDelete
	keyCtrl
	keyShift
	keyLeft
	keyPgUp
	keyPgDn
)

func (x *xtest) fake(typ, detail uint8, rx, ry int) (time.Time, error) {
	b := xreq(x.major, 2, 36)
	b[4] = typ
	b[5] = detail
	le.PutUint16(b[24:], uint16(int16(rx)))
	le.PutUint16(b[26:], uint16(int16(ry)))
	t := time.Now() // the instant just before the request is handed to the kernel
	_, err := x.c.send(b)
	return t, err
}

func (x *xtest) keyDown(code uint8) (time.Time, error) { return x.fake(2, code, 0, 0) }
func (x *xtest) keyUp(code uint8) (time.Time, error)   { return x.fake(3, code, 0, 0) }
func (x *xtest) motion(rx, ry int) error               { _, err := x.fake(6, 0, rx, ry); return err }
func (x *xtest) button(b uint8, down bool) error {
	t := uint8(5)
	if down {
		t = 4
	}
	_, err := x.fake(t, b, 0, 0)
	return err
}

func (x *xtest) code(ch rune) (keyChord, error) {
	k, ok := x.km[ch]
	if !ok {
		return keyChord{}, fmt.Errorf("no keycode for %q", ch)
	}
	return k, nil
}

// press sends the key-down of ch (with Shift held first when the character needs it) and returns the time of the
// key-down request that carries the character itself. release undoes it.
func (x *xtest) press(ch rune) (t time.Time, release func(), err error) {
	k, err := x.code(ch)
	if err != nil {
		return t, nil, err
	}
	var sh keyChord
	if k.shift {
		if sh, err = x.code(keyShift); err != nil {
			return t, nil, err
		}
		if _, err = x.keyDown(sh.code); err != nil {
			return t, nil, err
		}
	}
	t, err = x.keyDown(k.code)
	release = func() {
		x.keyUp(k.code)
		if k.shift {
			x.keyUp(sh.code)
		}
	}
	return t, release, err
}

// chord presses mod+ch (mod=keyCtrl) and returns the time of the final key-down.
func (x *xtest) chord(mod, ch rune) (time.Time, func(), error) {
	m, err := x.code(mod)
	if err != nil {
		return time.Time{}, nil, err
	}
	k, err := x.code(ch)
	if err != nil {
		return time.Time{}, nil, err
	}
	if _, err = x.keyDown(m.code); err != nil {
		return time.Time{}, nil, err
	}
	t, err := x.keyDown(k.code)
	return t, func() { x.keyUp(k.code); x.keyUp(m.code) }, err
}

// tap presses and releases ch with a short hold.
func (x *xtest) tap(ch rune) error {
	_, rel, err := x.press(ch)
	if err != nil {
		return err
	}
	time.Sleep(8 * time.Millisecond)
	rel()
	return nil
}

func (x *xtest) chordTap(mod, ch rune) error {
	_, rel, err := x.chord(mod, ch)
	if err != nil {
		return err
	}
	time.Sleep(8 * time.Millisecond)
	rel()
	return nil
}

// typeString types s with a fixed per-key gap (used for URLs, not measured).
func (x *xtest) typeString(s string, gap time.Duration) error {
	for _, ch := range s {
		if err := x.tap(ch); err != nil {
			return err
		}
		time.Sleep(gap)
	}
	return nil
}

// ---- DAMAGE -----------------------------------------------------------------------------------------------------

type damage struct {
	c       *xconn
	evBase  uint8
	major   uint8
	damage  uint32
	drawing uint32
}

func newDamage(c *xconn) (*damage, error) {
	e, err := c.extension("DAMAGE")
	if err != nil {
		return nil, err
	}
	v := xreq(e.major, 0, 12)
	le.PutUint32(v[4:], 1)
	le.PutUint32(v[8:], 1)
	if _, err := c.call(v); err != nil {
		return nil, err
	}
	d := &damage{c: c, evBase: e.firstEv, major: e.major, drawing: c.root}
	d.damage = c.newID()
	b := xreq(e.major, 1, 16)
	le.PutUint32(b[4:], d.damage)
	le.PutUint32(b[8:], c.root)
	b[12] = 0 // DamageReportRawRectangles: one event per damaged rectangle, no subtract needed
	if _, err := c.send(b); err != nil {
		return nil, err
	}
	return d, c.sync()
}

// notify decodes a DamageNotify; ok=false for other events.
func (d *damage) notify(ev []byte) (r rect, ok bool) {
	if ev[0]&0x7f != d.evBase {
		return rect{}, false
	}
	return rect{int(int16(le.Uint16(ev[16:]))), int(int16(le.Uint16(ev[18:]))), int(le.Uint16(ev[20:])), int(le.Uint16(ev[22:]))}, true
}

// subtract clears the accumulated damage region (keeps the server's region small).
func (d *damage) subtract() {
	b := xreq(d.major, 2, 16)
	le.PutUint32(b[4:], d.damage)
	d.c.send(b)
}

// ---- a known instant-changing X client (calibration) ------------------------------------------------------------------------

// flasher owns a small override-redirect window whose background it can flip between two colours in one request
// pair (ChangeWindowAttributes + ClearArea). The server paints it synchronously while processing the request, so the
// moment the request is written is the true instant of the pixel change, up to the server's own scheduling.
type flasher struct {
	c   *xconn
	win uint32
	r   rect
	cur bool
}

func newFlasher(c *xconn, r rect) (*flasher, error) {
	f := &flasher{c: c, r: r, win: c.newID()}
	b := xreq(opCreateWindow, 0, 32+8)
	b[1] = 0 // depth CopyFromParent
	le.PutUint32(b[4:], f.win)
	le.PutUint32(b[8:], c.root)
	le.PutUint16(b[12:], uint16(int16(r.x)))
	le.PutUint16(b[14:], uint16(int16(r.y)))
	le.PutUint16(b[16:], uint16(r.w))
	le.PutUint16(b[18:], uint16(r.h))
	le.PutUint16(b[22:], 1)          // class InputOutput
	le.PutUint32(b[28:], 0x2|0x200)  // CWBackPixel | CWOverrideRedirect
	le.PutUint32(b[32:], 0x00112233) // back pixel
	le.PutUint32(b[36:], 1)          // override redirect
	if _, err := c.send(b); err != nil {
		return nil, err
	}
	m := xreq(opMapWindow, 0, 8)
	le.PutUint32(m[4:], f.win)
	if _, err := c.send(m); err != nil {
		return nil, err
	}
	return f, c.sync()
}

// flip changes the colour and returns the instant the request was written.
func (f *flasher) flip() time.Time {
	f.cur = !f.cur
	col := uint32(0x00112233)
	if f.cur {
		col = 0x00ddeeff
	}
	b := xreq(opChangeWinAttrs, 0, 16)
	le.PutUint32(b[4:], f.win)
	le.PutUint32(b[8:], 0x2)
	le.PutUint32(b[12:], col)
	cl := xreq(opClearArea, 0, 16)
	le.PutUint32(cl[4:], f.win)
	both := append(b, cl...)
	t := time.Now()
	f.c.seq += 2
	f.c.nc.Write(both)
	return t
}

func (f *flasher) destroy() {
	b := xreq(opDestroyWindow, 0, 8)
	le.PutUint32(b[4:], f.win)
	f.c.send(b)
	f.c.sync()
}

var _ = os.Getenv
