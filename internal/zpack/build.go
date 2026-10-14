package zpack

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// This file is the write side, used by tools/zpack and by tests. Nothing in the browser calls it, so the linker
// drops it.

func deflate(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// Deflate encodes b as a blob. The result is never longer than a stored encoding would be plus a few bytes, and for
// empty input it is the 2-byte empty stream.
func Deflate(b []byte) []byte { return deflate(b) }

// Build encodes the named files as an archive. Entries with identical contents share one blob.
func Build(files map[string][]byte) []byte {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var blobs [][]byte // stored form
	var raws []int
	seen := map[[32]byte]int{}
	idx := make([]int, len(names))
	for i, n := range names {
		sum := sha256.Sum256(files[n])
		b, ok := seen[sum]
		if !ok {
			z := deflate(files[n])
			if len(z) >= len(files[n]) {
				z = files[n] // stored
			}
			b = len(blobs)
			seen[sum] = b
			blobs = append(blobs, z)
			raws = append(raws, len(files[n]))
		}
		idx[i] = b
	}
	var out bytes.Buffer
	out.WriteString("ZAR1")
	put := func(v int) {
		var t [binary.MaxVarintLen64]byte
		out.Write(t[:binary.PutUvarint(t[:], uint64(v))])
	}
	put(len(blobs))
	for i, b := range blobs {
		put(raws[i])
		put(len(b))
	}
	put(len(names))
	for i, n := range names {
		put(len(n))
		out.WriteString(n)
		put(idx[i])
	}
	for _, b := range blobs {
		out.Write(b)
	}
	return out.Bytes()
}
