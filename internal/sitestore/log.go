// Package sitestore is the on-disk side of the origin-scoped web storage APIs
// (IndexedDB, Cache Storage, service worker registrations). It knows nothing
// about JavaScript: the engine's host functions drive it with already
// serialised values and order-preserving key strings.
//
// Everything persistent here is an append-only record log (see recLog) or a
// plain file written to a temporary name and renamed into place.
package sitestore

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// recLog is an append-only file of records: [u32 length][u32 CRC-32C][payload].
// A torn or corrupt tail (power loss in the middle of an append) is detected on
// open and cut off, so a reader sees every complete record and nothing else.
// A nil *recLog is a memory-only log: appends are accepted and dropped.
type recLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

const recHeader = 8

// openLog opens (creating if asked) the log at path and returns its records.
func openLog(path string, create bool) (*recLog, [][]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) || !create {
			return nil, nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, nil, err
		}
		b = nil
	}
	var recs [][]byte
	off := 0
	for off+recHeader <= len(b) {
		n := int(binary.LittleEndian.Uint32(b[off:]))
		sum := binary.LittleEndian.Uint32(b[off+4:])
		if n < 0 || off+recHeader+n > len(b) {
			break
		}
		p := b[off+recHeader : off+recHeader+n]
		if crc32.Checksum(p, crcTable) != sum {
			break
		}
		recs = append(recs, p)
		off += recHeader + n
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if int64(off) != int64(len(b)) { // cut the torn tail
		if err := f.Truncate(int64(off)); err != nil {
			f.Close()
			return nil, nil, err
		}
	}
	if _, err := f.Seek(int64(off), io.SeekStart); err != nil {
		f.Close()
		return nil, nil, err
	}
	return &recLog{path: path, f: f, size: int64(off)}, recs, nil
}

func (l *recLog) append(p []byte) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return errors.New("log closed")
	}
	buf := make([]byte, recHeader+len(p))
	binary.LittleEndian.PutUint32(buf, uint32(len(p)))
	binary.LittleEndian.PutUint32(buf[4:], crc32.Checksum(p, crcTable))
	copy(buf[recHeader:], p)
	n, err := l.f.Write(buf)
	l.size += int64(n)
	if err != nil {
		// leave no half record behind: later appends must not follow it
		l.size -= int64(n)
		_ = l.f.Truncate(l.size)
		_, _ = l.f.Seek(l.size, io.SeekStart)
	}
	return err
}

// rewrite replaces the whole log with recs: written to a temporary file,
// synced, then renamed over the old one.
func (l *recLog) rewrite(recs [][]byte) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	tmp := l.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	var size int64
	hdr := make([]byte, recHeader)
	for _, p := range recs {
		binary.LittleEndian.PutUint32(hdr, uint32(len(p)))
		binary.LittleEndian.PutUint32(hdr[4:], crc32.Checksum(p, crcTable))
		if _, err = f.Write(hdr); err == nil {
			_, err = f.Write(p)
		}
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		size += int64(recHeader + len(p))
	}
	if err = f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err = f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	if err = os.Rename(tmp, l.path); err != nil {
		os.Remove(tmp)
		return err
	}
	nf, err := os.OpenFile(l.path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if _, err = nf.Seek(size, io.SeekStart); err != nil {
		nf.Close()
		return err
	}
	l.f, l.size = nf, size
	return nil
}

func (l *recLog) sync() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.f != nil {
		_ = l.f.Sync()
	}
	l.mu.Unlock()
}

func (l *recLog) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.f != nil {
		_ = l.f.Sync()
		_ = l.f.Close()
		l.f = nil
	}
	l.mu.Unlock()
}

func (l *recLog) length() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size
}

func (l *recLog) remove() {
	if l == nil {
		return
	}
	l.close()
	_ = os.Remove(l.path)
}

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
