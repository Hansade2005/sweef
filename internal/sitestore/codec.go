package sitestore

import (
	"encoding/binary"
	"errors"
)

// ---- tiny binary codec ----------------------------------------------------------

type wbuf struct{ b []byte }

func (w *wbuf) u(v uint64)  { w.b = binary.AppendUvarint(w.b, v) }
func (w *wbuf) i(v int64)   { w.b = binary.AppendVarint(w.b, v) }
func (w *wbuf) byte(v byte) { w.b = append(w.b, v) }
func (w *wbuf) s(v string)  { w.u(uint64(len(v))); w.b = append(w.b, v...) }
func (w *wbuf) bool(v bool) {
	if v {
		w.byte(1)
	} else {
		w.byte(0)
	}
}

type rbuf struct {
	b   []byte
	err error
}

var errCorrupt = errors.New("sitestore: corrupt record")

func (r *rbuf) u() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errCorrupt
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *rbuf) i() int64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Varint(r.b)
	if n <= 0 {
		r.err = errCorrupt
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *rbuf) byte() byte {
	if r.err != nil || len(r.b) == 0 {
		r.err = errCorrupt
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *rbuf) s() string {
	n := r.u()
	if r.err != nil || uint64(len(r.b)) < n {
		r.err = errCorrupt
		return ""
	}
	v := string(r.b[:n])
	r.b = r.b[n:]
	return v
}

func (r *rbuf) bool() bool { return r.byte() != 0 }
func (r *rbuf) more() bool { return r.err == nil && len(r.b) > 0 }
