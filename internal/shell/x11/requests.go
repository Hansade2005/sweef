//go:build linux

package x11

import (
	"errors"
	"unicode/utf8"
)

// Core protocol opcodes.
const (
	opCreateWindow      = 1
	opChangeWindowAttrs = 2
	opDestroyWindow     = 4
	opMapWindow         = 8
	opConfigureWindow   = 12
	opGetGeometry       = 14
	opInternAtom        = 16
	opChangeProperty    = 18
	opDeleteProperty    = 19
	opGetProperty       = 20
	opSetSelectionOwner = 22
	opGetSelectionOwner = 23
	opConvertSelection  = 24
	opSendEvent         = 25
	opGetInputFocus     = 43
	opOpenFont          = 45
	opCreateGC          = 55
	opFreeGC            = 60
	opPutImage          = 72
	opGetImage          = 73
	opFreeCursor        = 95
	opCreateGlyphCursor = 94
	opQueryExtension    = 98
	opGetKbdMapping     = 101
	opGetModMapping     = 119
)

// Event types.
const (
	evKeyPress         = 2
	evKeyRelease       = 3
	evButtonPress      = 4
	evButtonRelease    = 5
	evMotionNotify     = 6
	evEnterNotify      = 7
	evLeaveNotify      = 8
	evFocusIn          = 9
	evFocusOut         = 10
	evExpose           = 12
	evDestroyNotify    = 17
	evConfigureNotify  = 22
	evPropertyNotify   = 28
	evSelectionClear   = 29
	evSelectionRequest = 30
	evSelectionNotify  = 31
	evClientMessage    = 33
	evMappingNotify    = 34
)

// Event mask bits.
const (
	maskKeyPress       = 1 << 0
	maskKeyRelease     = 1 << 1
	maskButtonPress    = 1 << 2
	maskButtonRelease  = 1 << 3
	maskEnterWindow    = 1 << 4
	maskLeaveWindow    = 1 << 5
	maskPointerMotion  = 1 << 6
	maskExposure       = 1 << 15
	maskStructure      = 1 << 17
	maskSubstructNotif = 1 << 19
	maskSubstructRedir = 1 << 20
	maskFocusChange    = 1 << 21
	maskPropertyChange = 1 << 22
)

// Predefined atoms.
const (
	atomPrimary         = 1
	atomAtom            = 4
	atomCardinal        = 6
	atomString          = 31
	atomWMName          = 39
	atomWMNormalHints   = 40
	atomWMClass         = 67
	atomResourceManager = 23
	atomWindow          = 33
)

func (c *conn) intern(name string, onlyIfExists bool) *call {
	b := request(opInternAtom, 0, 8+len(name))
	if onlyIfExists {
		b[1] = 1
	}
	le.PutUint16(b[4:], uint16(len(name)))
	copy(b[8:], name)
	cl, _ := c.send(b, true)
	return cl
}

// internAll interns names concurrently (one round trip).
func (c *conn) internAll(names []string) (map[string]uint32, error) {
	calls := make([]*call, len(names))
	for i, n := range names {
		calls[i] = c.intern(n, false)
		if calls[i] == nil {
			return nil, errors.New("x11: intern failed")
		}
	}
	m := make(map[string]uint32, len(names))
	for i, cl := range calls {
		r, err := cl.Wait()
		if err != nil {
			return nil, err
		}
		m[names[i]] = le.Uint32(r[8:])
	}
	return m, nil
}

func (c *conn) queryExtension(name string) (extInfo, error) {
	c.extMu.Lock()
	if e, ok := c.exts[name]; ok {
		c.extMu.Unlock()
		return e, nil
	}
	c.extMu.Unlock()
	b := request(opQueryExtension, 0, 8+len(name))
	le.PutUint16(b[4:], uint16(len(name)))
	copy(b[8:], name)
	r, err := c.do(b)
	if err != nil {
		return extInfo{}, err
	}
	e := extInfo{present: r[8] != 0, major: r[9], firstEvent: r[10], firstErr: r[11]}
	c.extMu.Lock()
	c.exts[name] = e
	c.extMu.Unlock()
	return e, nil
}

// changeProperty sets a property. format is 8, 16 or 32; data is raw bytes
// (little-endian for 16/32).
func (c *conn) changeProperty(mode uint8, win, prop, typ uint32, format uint8, data []byte) error {
	b := request(opChangeProperty, mode, 24+len(data))
	le.PutUint32(b[4:], win)
	le.PutUint32(b[8:], prop)
	le.PutUint32(b[12:], typ)
	b[16] = format
	le.PutUint32(b[20:], uint32(len(data))/uint32(format/8))
	copy(b[24:], data)
	_, err := c.send(b, false)
	return err
}

func u32s(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		le.PutUint32(b[4*i:], x)
	}
	return b
}

func (c *conn) deleteProperty(win, prop uint32) error {
	b := request(opDeleteProperty, 0, 12)
	le.PutUint32(b[4:], win)
	le.PutUint32(b[8:], prop)
	_, err := c.send(b, false)
	return err
}

type propReply struct {
	typ    uint32
	format uint8
	after  uint32
	value  []byte
}

// getProperty reads up to maxBytes of a property (typ 0 = AnyPropertyType).
func (c *conn) getProperty(win, prop, typ uint32, del bool, offsetWords, maxWords uint32) (*propReply, error) {
	b := request(opGetProperty, 0, 24)
	if del {
		b[1] = 1
	}
	le.PutUint32(b[4:], win)
	le.PutUint32(b[8:], prop)
	le.PutUint32(b[12:], typ)
	le.PutUint32(b[16:], offsetWords)
	le.PutUint32(b[20:], maxWords)
	r, err := c.do(b)
	if err != nil {
		return nil, err
	}
	pr := &propReply{format: r[1], typ: le.Uint32(r[8:]), after: le.Uint32(r[12:])}
	n := int(le.Uint32(r[16:]))
	if pr.format != 0 {
		n *= int(pr.format / 8)
	}
	if 32+n <= len(r) {
		pr.value = r[32 : 32+n]
	}
	return pr, nil
}

// getPropertyAll reads a whole property (looping while bytes remain),
// capped at limit bytes.
func (c *conn) getPropertyAll(win, prop, typ uint32, del bool, limit int) (*propReply, error) {
	const chunkWords = 1 << 18 // 1 MiB per request
	var out *propReply
	var off uint32
	for {
		pr, err := c.getProperty(win, prop, typ, del && false, off, chunkWords)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = pr
			out.value = append([]byte(nil), pr.value...)
		} else {
			out.value = append(out.value, pr.value...)
		}
		out.after = pr.after
		if pr.after == 0 || len(out.value) >= limit || len(pr.value) == 0 {
			break
		}
		off += uint32(len(pr.value) / 4)
	}
	if del {
		c.deleteProperty(win, prop)
	}
	return out, nil
}

func (c *conn) sendEvent(dest uint32, propagate bool, mask uint32, ev []byte) error {
	b := request(opSendEvent, 0, 44)
	if propagate {
		b[1] = 1
	}
	le.PutUint32(b[4:], dest)
	le.PutUint32(b[8:], mask)
	copy(b[12:44], ev)
	_, err := c.send(b, false)
	return err
}

// clientMessage32 builds a 32-bit-format ClientMessage event.
func clientMessage32(win, typ uint32, d [5]uint32) []byte {
	ev := make([]byte, 32)
	ev[0] = evClientMessage
	ev[1] = 32
	le.PutUint32(ev[4:], win)
	le.PutUint32(ev[8:], typ)
	for i, v := range d {
		le.PutUint32(ev[12+4*i:], v)
	}
	return ev
}

func (c *conn) setSelectionOwner(owner, sel, t uint32) error {
	b := request(opSetSelectionOwner, 0, 16)
	le.PutUint32(b[4:], owner)
	le.PutUint32(b[8:], sel)
	le.PutUint32(b[12:], t)
	_, err := c.send(b, false)
	return err
}

func (c *conn) getSelectionOwner(sel uint32) (uint32, error) {
	b := request(opGetSelectionOwner, 0, 8)
	le.PutUint32(b[4:], sel)
	r, err := c.do(b)
	if err != nil {
		return 0, err
	}
	return le.Uint32(r[8:]), nil
}

func (c *conn) convertSelection(requestor, sel, target, prop, t uint32) error {
	b := request(opConvertSelection, 0, 24)
	le.PutUint32(b[4:], requestor)
	le.PutUint32(b[8:], sel)
	le.PutUint32(b[12:], target)
	le.PutUint32(b[16:], prop)
	le.PutUint32(b[20:], t)
	_, err := c.send(b, false)
	return err
}

// sync performs a round trip so all earlier requests (and their errors)
// have been processed by the server.
func (c *conn) sync() error {
	_, err := c.do(request(opGetInputFocus, 0, 4))
	return err
}

func (c *conn) mapWindow(w uint32) error {
	b := request(opMapWindow, 0, 8)
	le.PutUint32(b[4:], w)
	_, err := c.send(b, false)
	return err
}

func (c *conn) destroyWindow(w uint32) error {
	b := request(opDestroyWindow, 0, 8)
	le.PutUint32(b[4:], w)
	_, err := c.send(b, false)
	return err
}

// configureWindow sets geometry; mask bits: x=1 y=2 w=4 h=8.
func (c *conn) configureWindow(w uint32, mask uint16, vals ...uint32) error {
	b := request(opConfigureWindow, 0, 12+4*len(vals))
	le.PutUint32(b[4:], w)
	le.PutUint16(b[8:], mask)
	for i, v := range vals {
		le.PutUint32(b[12+4*i:], v)
	}
	_, err := c.send(b, false)
	return err
}

func (c *conn) changeWindowAttrs(w uint32, mask uint32, vals ...uint32) error {
	b := request(opChangeWindowAttrs, 0, 12+4*len(vals))
	le.PutUint32(b[4:], w)
	le.PutUint32(b[8:], mask)
	for i, v := range vals {
		le.PutUint32(b[12+4*i:], v)
	}
	_, err := c.send(b, false)
	return err
}

// getImage reads a rectangle of a drawable as ZPixmap bytes.
func (c *conn) getImage(d uint32, x, y, w, h int) ([]byte, error) {
	b := request(opGetImage, 2, 20)
	le.PutUint32(b[4:], d)
	le.PutUint16(b[8:], uint16(int16(x)))
	le.PutUint16(b[10:], uint16(int16(y)))
	le.PutUint16(b[12:], uint16(w))
	le.PutUint16(b[14:], uint16(h))
	le.PutUint32(b[16:], 0xffffffff)
	r, err := c.do(b)
	if err != nil {
		return nil, err
	}
	return r[32:], nil
}

// latin1 converts s for a STRING-typed property, replacing unmappable runes.
func latin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			r = '?'
		}
		out = append(out, byte(r))
	}
	return out
}

func fromLatin1(b []byte) string {
	if utf8.Valid(b) {
		allASCII := true
		for _, c := range b {
			if c >= 0x80 {
				allASCII = false
				break
			}
		}
		if allASCII {
			return string(b)
		}
	}
	rs := make([]rune, len(b))
	for i, c := range b {
		rs[i] = rune(c)
	}
	return string(rs)
}
