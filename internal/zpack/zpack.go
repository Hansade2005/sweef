// Package zpack holds the compact form of the data that travels inside the Sweef binary: JavaScript preludes, the
// chrome UI assets, the IANA time zone database. Text compresses to a quarter of its size, and the binary has a size
// budget (docs/development.md, "Binary size budget"), so these are embedded deflated and inflated on first use.
//
// Two shapes exist:
//
//   - a blob: one raw DEFLATE stream (RFC 1951, compress/flate), file name "<source>.z" next to its source. Inflate
//     and String decode it. The sources are not embedded, only the .z files are.
//   - an archive ("ZAR1"): many named blobs behind an index, with identical contents stored once (the time zone
//     database has hundreds of aliases). Open parses the index without copying; Archive.Read inflates one entry.
//
// Archive layout, all integers unsigned LEB128 (encoding/binary Uvarint):
//
//	"ZAR1"
//	nBlobs, then per blob: rawLen, storedLen        (storedLen == rawLen: the bytes are stored, not deflated)
//	nEntries, then per entry: nameLen, name, blobIndex
//	the blobs' bytes, concatenated in blob order
//
// Entries are sorted by name. The generator is tools/zpack; each consumer has a test that decodes everything and
// compares it with the source files, so a stale .z/.zar file fails the build's tests instead of shipping.
//
// Only the read side is reachable from the browser: the writer (build.go) is dead code in the binary and is
// removed by the linker.
package zpack

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"sort"
	"sync"
)

// Inflate decodes a blob made by Deflate. The embedded data is generated and tested, so corruption is a bug in the
// build, not a runtime condition: Inflate panics instead of returning an error nobody could act on.
func Inflate(z string) []byte {
	b, err := inflate(z, -1)
	if err != nil {
		panic("zpack: " + err.Error())
	}
	return b
}

// String is Inflate as a string.
func String(z string) string { return string(Inflate(z)) }

func inflate(z string, sizeHint int) ([]byte, error) {
	r := flate.NewReader(stringReader{&z})
	defer r.Close()
	var buf bytes.Buffer
	if sizeHint > 0 {
		buf.Grow(sizeHint)
	} else {
		buf.Grow(len(z) * 4)
	}
	if _, err := io.Copy(&buf, r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// stringReader reads a string without copying it (strings.Reader would do too; this one is also an io.ByteReader
// so that flate does not wrap it in a bufio.Reader).
type stringReader struct{ s *string }

func (r stringReader) Read(p []byte) (int, error) {
	if len(*r.s) == 0 {
		return 0, io.EOF
	}
	n := copy(p, *r.s)
	*r.s = (*r.s)[n:]
	return n, nil
}

func (r stringReader) ReadByte() (byte, error) {
	if len(*r.s) == 0 {
		return 0, io.EOF
	}
	c := (*r.s)[0]
	*r.s = (*r.s)[1:]
	return c, nil
}

// Lazy inflates a blob once and keeps the text. The zero value is ready; declare one per embedded blob:
//
//	//go:embed scope.js.z
//	var scopeZ string
//	var scope zpack.Lazy   // scope.Get(scopeZ)
type Lazy struct {
	once sync.Once
	s    string
}

// Get returns the decoded text of z, inflating it on the first call (later calls must pass the same z).
func (l *Lazy) Get(z string) string {
	l.once.Do(func() { l.s = String(z) })
	return l.s
}

// Archive is a parsed archive. The names and blobs alias the string it was opened from.
type Archive struct {
	names []string // sorted
	blob  []int    // blob index of each name
	raw   []int    // raw length of each blob
	data  []string // stored bytes of each blob
}

var errFormat = errors.New("bad archive")

// Open parses the index of an archive.
func Open(z string) (*Archive, error) {
	if len(z) < 4 || z[:4] != "ZAR1" {
		return nil, errFormat
	}
	p := 4
	uv := func() (int, error) {
		v, n := binary.Uvarint([]byte(z[p:min(len(z), p+10)]))
		if n <= 0 || v > 1<<31 {
			return 0, errFormat
		}
		p += n
		return int(v), nil
	}
	nb, err := uv()
	if err != nil {
		return nil, err
	}
	a := &Archive{raw: make([]int, nb), data: make([]string, nb)}
	stored := make([]int, nb)
	for i := range stored {
		if a.raw[i], err = uv(); err != nil {
			return nil, err
		}
		if stored[i], err = uv(); err != nil {
			return nil, err
		}
	}
	ne, err := uv()
	if err != nil {
		return nil, err
	}
	a.names, a.blob = make([]string, ne), make([]int, ne)
	for i := range a.names {
		l, err := uv()
		if err != nil || p+l > len(z) {
			return nil, errFormat
		}
		a.names[i] = z[p : p+l]
		p += l
		if a.blob[i], err = uv(); err != nil || a.blob[i] >= nb {
			return nil, errFormat
		}
	}
	for i := range a.data {
		if p+stored[i] > len(z) {
			return nil, errFormat
		}
		a.data[i] = z[p : p+stored[i]]
		p += stored[i]
	}
	return a, nil
}

// Names lists the entries in sorted order. The slice must not be modified.
func (a *Archive) Names() []string { return a.names }

func (a *Archive) index(name string) int {
	i := sort.SearchStrings(a.names, name)
	if i < len(a.names) && a.names[i] == name {
		return i
	}
	return -1
}

// Size is the decoded length of an entry.
func (a *Archive) Size(name string) (int, bool) {
	i := a.index(name)
	if i < 0 {
		return 0, false
	}
	return a.raw[a.blob[i]], true
}

// Read returns a fresh copy of an entry's bytes.
func (a *Archive) Read(name string) ([]byte, bool) {
	i := a.index(name)
	if i < 0 {
		return nil, false
	}
	b := a.blob[i]
	if len(a.data[b]) == a.raw[b] {
		return []byte(a.data[b]), true
	}
	out, err := inflate(a.data[b], a.raw[b])
	if err != nil || len(out) != a.raw[b] {
		panic("zpack: corrupt archive entry " + name)
	}
	return out, true
}
