package sitestore

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"unicode/utf8"
)

// IndexedDB keys.
//
// The script side (idb_prelude.js) hands keys to Go in a "wire" form: a string
// whose characters are all <= U+00FF, so it is a byte string in disguise, whose
// plain byte order is already the IndexedDB key order. The wire form is
// convenient for JavaScript (hex digits for numbers, no NUL bytes) and
// wasteful on disk (17 bytes for a number). The store therefore keeps every key
// in a compact "storage" form and converts at the boundary:
//
//	wire form    ->  storage form (this file: transcode)  ->  bytes in the ordered KV store
//
// Both forms are memcmp-ordered: for any two valid keys a and b,
// CompareKeys(a, b) has the same sign as bytes.Compare of their encodings.
//
// Storage form (self-delimiting, so no encoding is a prefix of another):
//
//	number  0x10 + 8 bytes: IEEE-754 bits, big endian, sign-corrected (-0 is written as +0)
//	date    0x20 + the same 8 bytes for the time value
//	string  0x30 + the UTF-16 code units, each as 1-3 "UTF-8 style" bytes (so order is by code unit,
//	        not by code point), unit 0 as 00 FF, then 00 01
//	binary  0x40 + the bytes, byte 0 as 00 FF, then 00 01
//	array   0x50 + the encoded elements + 00
//
// The terminators sort before every payload byte (00 01 < 00 FF < 01..FF) and 00
// ends an array because every element starts with a tag >= 0x10, so an array
// that is a prefix of another sorts first, exactly like the specification's
// element-wise comparison. BigInt, NaN and invalid dates are not keys.

// Key kinds in IndexedDB order.
const (
	KeyNumber byte = 0x10
	KeyDate   byte = 0x20
	KeyString byte = 0x30
	KeyBinary byte = 0x40
	KeyArray  byte = 0x50
)

// Key is a typed IndexedDB key, the reference form against which the byte
// encodings are tested.
type Key struct {
	Kind byte
	Num  float64  // KeyNumber, KeyDate
	Str  []uint16 // KeyString: UTF-16 code units
	Bin  []byte   // KeyBinary
	Arr  []Key    // KeyArray
}

// ErrInvalidKey is returned for NaN numbers, invalid dates and malformed encodings.
var ErrInvalidKey = errors.New("sitestore: not a valid IndexedDB key")

// NumberKey, DateKey, StringKey, BinaryKey and ArrayKey build keys.
func NumberKey(f float64) Key   { return Key{Kind: KeyNumber, Num: f} }
func DateKey(ms float64) Key    { return Key{Kind: KeyDate, Num: ms} }
func BinaryKey(b []byte) Key    { return Key{Kind: KeyBinary, Bin: b} }
func ArrayKey(elems ...Key) Key { return Key{Kind: KeyArray, Arr: elems} }
func StringKey(s string) Key {
	u := make([]uint16, 0, len(s))
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			u = append(u, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			u = append(u, uint16(r))
		}
	}
	return Key{Kind: KeyString, Str: u}
}

// Valid reports whether the key (and every element) is a valid IndexedDB key.
func (k Key) Valid() bool {
	switch k.Kind {
	case KeyNumber, KeyDate:
		return !math.IsNaN(k.Num)
	case KeyString, KeyBinary:
		return true
	case KeyArray:
		for _, e := range k.Arr {
			if !e.Valid() {
				return false
			}
		}
		return true
	}
	return false
}

// CompareKeys is the specification's "compare two keys": number < date <
// string < binary < array; numbers and dates by value, strings by UTF-16 code
// unit, binaries by byte, arrays element by element and then by length.
// It is the oracle the encodings are tested against.
func CompareKeys(a, b Key) int {
	if a.Kind != b.Kind {
		if a.Kind < b.Kind {
			return -1
		}
		return 1
	}
	switch a.Kind {
	case KeyNumber, KeyDate:
		x, y := a.Num, b.Num // -0 == +0 under < and >
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case KeyString:
		n := len(a.Str)
		if len(b.Str) < n {
			n = len(b.Str)
		}
		for i := 0; i < n; i++ {
			if a.Str[i] != b.Str[i] {
				if a.Str[i] < b.Str[i] {
					return -1
				}
				return 1
			}
		}
		return cmpInt(len(a.Str), len(b.Str))
	case KeyBinary:
		return bytes.Compare(a.Bin, b.Bin)
	case KeyArray:
		n := len(a.Arr)
		if len(b.Arr) < n {
			n = len(b.Arr)
		}
		for i := 0; i < n; i++ {
			if c := CompareKeys(a.Arr[i], b.Arr[i]); c != 0 {
				return c
			}
		}
		return cmpInt(len(a.Arr), len(b.Arr))
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// sortableBits maps a float to a uint64 whose unsigned order is the numeric order.
func sortableBits(f float64) uint64 {
	if f == 0 {
		f = 0 // -0 is 0
	}
	b := math.Float64bits(f)
	if b>>63 != 0 {
		return ^b
	}
	return b | 1<<63
}

func fromSortableBits(b uint64) float64 {
	if b>>63 != 0 {
		return math.Float64frombits(b &^ (1 << 63))
	}
	return math.Float64frombits(^b)
}

// unitBytes appends a UTF-16 code unit in the order-preserving "UTF-8 style".
func appendUnit(dst []byte, u uint16) []byte {
	switch {
	case u == 0:
		return append(dst, 0x00, 0xFF)
	case u < 0x80:
		return append(dst, byte(u))
	case u < 0x800:
		return append(dst, 0xC0|byte(u>>6), 0x80|byte(u&63))
	}
	return append(dst, 0xE0|byte(u>>12), 0x80|byte((u>>6)&63), 0x80|byte(u&63))
}

// AppendStorage appends the storage form of the key. The key must be Valid.
func (k Key) AppendStorage(dst []byte) []byte {
	switch k.Kind {
	case KeyNumber, KeyDate:
		dst = append(dst, k.Kind)
		return binary.BigEndian.AppendUint64(dst, sortableBits(k.Num))
	case KeyString:
		dst = append(dst, KeyString)
		for _, u := range k.Str {
			dst = appendUnit(dst, u)
		}
		return append(dst, 0x00, 0x01)
	case KeyBinary:
		dst = append(dst, KeyBinary)
		for _, c := range k.Bin {
			if c == 0 {
				dst = append(dst, 0x00, 0xFF)
			} else {
				dst = append(dst, c)
			}
		}
		return append(dst, 0x00, 0x01)
	case KeyArray:
		dst = append(dst, KeyArray)
		for _, e := range k.Arr {
			dst = e.AppendStorage(dst)
		}
		return append(dst, 0x00)
	}
	return dst
}

// Storage returns the storage form of the key.
func (k Key) Storage() []byte { return k.AppendStorage(nil) }

// DecodeStorageKey reads one key from the front of b and returns it with the bytes consumed.
func DecodeStorageKey(b []byte) (Key, int, error) {
	k, n, err := decodeStorage(b, 0)
	return k, n, err
}

func decodeStorage(b []byte, depth int) (Key, int, error) {
	if len(b) == 0 || depth > 64 {
		return Key{}, 0, ErrInvalidKey
	}
	switch b[0] {
	case KeyNumber, KeyDate:
		if len(b) < 9 {
			return Key{}, 0, ErrInvalidKey
		}
		return Key{Kind: b[0], Num: fromSortableBits(binary.BigEndian.Uint64(b[1:9]))}, 9, nil
	case KeyString:
		var units []uint16
		i := 1
		for {
			if i >= len(b) {
				return Key{}, 0, ErrInvalidKey
			}
			c := b[i]
			switch {
			case c == 0:
				if i+1 >= len(b) {
					return Key{}, 0, ErrInvalidKey
				}
				if b[i+1] == 0x01 {
					return Key{Kind: KeyString, Str: units}, i + 2, nil
				}
				if b[i+1] != 0xFF {
					return Key{}, 0, ErrInvalidKey
				}
				units = append(units, 0)
				i += 2
			case c < 0x80:
				units = append(units, uint16(c))
				i++
			case c < 0xE0:
				if i+1 >= len(b) {
					return Key{}, 0, ErrInvalidKey
				}
				units = append(units, uint16(c&31)<<6|uint16(b[i+1]&63))
				i += 2
			default:
				if i+2 >= len(b) {
					return Key{}, 0, ErrInvalidKey
				}
				units = append(units, uint16(c&15)<<12|uint16(b[i+1]&63)<<6|uint16(b[i+2]&63))
				i += 3
			}
		}
	case KeyBinary:
		var out []byte
		i := 1
		for {
			if i >= len(b) {
				return Key{}, 0, ErrInvalidKey
			}
			c := b[i]
			if c == 0 {
				if i+1 >= len(b) {
					return Key{}, 0, ErrInvalidKey
				}
				if b[i+1] == 0x01 {
					return Key{Kind: KeyBinary, Bin: out}, i + 2, nil
				}
				if b[i+1] != 0xFF {
					return Key{}, 0, ErrInvalidKey
				}
				out = append(out, 0)
				i += 2
				continue
			}
			out = append(out, c)
			i++
		}
	case KeyArray:
		var elems []Key
		i := 1
		for {
			if i >= len(b) {
				return Key{}, 0, ErrInvalidKey
			}
			if b[i] == 0x00 {
				return Key{Kind: KeyArray, Arr: elems}, i + 1, nil
			}
			e, n, err := decodeStorage(b[i:], depth+1)
			if err != nil {
				return Key{}, 0, err
			}
			elems = append(elems, e)
			i += n
		}
	}
	return Key{}, 0, ErrInvalidKey
}

// ---- the wire form (what idb_prelude.js produces) ---------------------------------------

// wireBytes turns a wire key (a string of characters <= U+00FF) into its bytes.
func wireBytes(s string) ([]byte, error) {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return []byte(s), nil
	}
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			return nil, ErrInvalidKey
		}
		out = append(out, byte(r))
	}
	return out, nil
}

// wireString is the inverse: one character per byte.
func wireString(b []byte) string {
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	var sb []byte
	sb = make([]byte, 0, len(b)*2)
	for _, c := range b {
		sb = utf8.AppendRune(sb, rune(c))
	}
	return string(sb)
}

const hexDigits = "0123456789abcdef"

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// WireToStorage converts a wire key to its storage form.
func WireToStorage(w string) ([]byte, error) {
	b, err := wireBytes(w)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(b)/2+4)
	n, out, err := transcodeIn(b, out, 0)
	if err != nil || n != len(b) {
		return nil, ErrInvalidKey
	}
	return out, nil
}

func transcodeIn(b, out []byte, depth int) (int, []byte, error) {
	if len(b) == 0 || depth > 64 {
		return 0, out, ErrInvalidKey
	}
	switch b[0] {
	case KeyNumber, KeyDate:
		if len(b) < 17 {
			return 0, out, ErrInvalidKey
		}
		out = append(out, b[0])
		for i := 1; i < 17; i += 2 {
			hi, ok1 := hexVal(b[i])
			lo, ok2 := hexVal(b[i+1])
			if !ok1 || !ok2 {
				return 0, out, ErrInvalidKey
			}
			out = append(out, hi<<4|lo)
		}
		return 17, out, nil
	case KeyString, KeyBinary:
		out = append(out, b[0])
		i := 1
		for {
			if i >= len(b) {
				return 0, out, ErrInvalidKey
			}
			c := b[i]
			switch c {
			case 0x01:
				return i + 1, append(out, 0x00, 0x01), nil
			case 0x02:
				if i+1 >= len(b) || b[i+1] < 1 || b[i+1] > 3 {
					return 0, out, ErrInvalidKey
				}
				if v := b[i+1] - 1; v == 0 {
					out = append(out, 0x00, 0xFF)
				} else {
					out = append(out, v)
				}
				i += 2
			case 0x00:
				return 0, out, ErrInvalidKey
			default:
				out = append(out, c)
				i++
			}
		}
	case KeyArray:
		out = append(out, KeyArray)
		i := 1
		for {
			if i >= len(b) {
				return 0, out, ErrInvalidKey
			}
			if b[i] == 0x01 {
				return i + 1, append(out, 0x00), nil
			}
			n, o2, err := transcodeIn(b[i:], out, depth+1)
			if err != nil {
				return 0, out, err
			}
			out = o2
			i += n
		}
	}
	return 0, out, ErrInvalidKey
}

// StorageToWire converts a storage key back to the wire form the script side decodes.
func StorageToWire(s []byte) (string, error) {
	out := make([]byte, 0, len(s)*2)
	n, out, err := transcodeOut(s, out, 0)
	if err != nil || n != len(s) {
		return "", ErrInvalidKey
	}
	return wireString(out), nil
}

func transcodeOut(b, out []byte, depth int) (int, []byte, error) {
	if len(b) == 0 || depth > 64 {
		return 0, out, ErrInvalidKey
	}
	switch b[0] {
	case KeyNumber, KeyDate:
		if len(b) < 9 {
			return 0, out, ErrInvalidKey
		}
		out = append(out, b[0])
		for _, c := range b[1:9] {
			out = append(out, hexDigits[c>>4], hexDigits[c&15])
		}
		return 9, out, nil
	case KeyString, KeyBinary:
		out = append(out, b[0])
		i := 1
		for {
			if i >= len(b) {
				return 0, out, ErrInvalidKey
			}
			c := b[i]
			if c == 0 {
				if i+1 >= len(b) {
					return 0, out, ErrInvalidKey
				}
				switch b[i+1] {
				case 0x01:
					return i + 2, append(out, 0x01), nil
				case 0xFF:
					out = append(out, 0x02, 0x01)
					i += 2
					continue
				}
				return 0, out, ErrInvalidKey
			}
			if c < 3 {
				out = append(out, 0x02, c+1)
			} else {
				out = append(out, c)
			}
			i++
		}
	case KeyArray:
		out = append(out, KeyArray)
		i := 1
		for {
			if i >= len(b) {
				return 0, out, ErrInvalidKey
			}
			if b[i] == 0x00 {
				return i + 1, append(out, 0x01), nil
			}
			n, o2, err := transcodeOut(b[i:], out, depth+1)
			if err != nil {
				return 0, out, err
			}
			out = o2
			i += n
		}
	}
	return 0, out, ErrInvalidKey
}

// Wire returns the wire form of the key, byte for byte what idb_prelude.js produces.
func (k Key) Wire() string {
	s := k.Storage()
	w, err := StorageToWire(s)
	if err != nil {
		return ""
	}
	return w
}

// ParseWire reads a wire key.
func ParseWire(w string) (Key, error) {
	s, err := WireToStorage(w)
	if err != nil {
		return Key{}, err
	}
	k, _, err := DecodeStorageKey(s)
	return k, err
}

// SortKeys sorts keys by CompareKeys (used by tests and tools).
func SortKeys(ks []Key) { sort.Slice(ks, func(i, j int) bool { return CompareKeys(ks[i], ks[j]) < 0 }) }
