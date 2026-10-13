package sitestore

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// randKey draws a key from a distribution that hits every awkward corner of
// the encoding: signed zeros, infinities, denormals, NUL / 0x01 / 0x02 bytes and
// units, surrogate halves, long shared prefixes, nested and empty arrays.
func randKey(r *rand.Rand, depth int) Key {
	kinds := 5
	if depth >= 3 {
		kinds = 4
	}
	switch r.Intn(kinds) {
	case 0:
		return NumberKey(randNum(r))
	case 1:
		return DateKey(randNum(r))
	case 2:
		return Key{Kind: KeyString, Str: randUnits(r)}
	case 3:
		n := r.Intn(6)
		b := make([]byte, n)
		for i := range b {
			switch r.Intn(4) {
			case 0:
				b[i] = byte(r.Intn(4)) // 0, 1, 2, 3
			case 1:
				b[i] = 0xFF
			default:
				b[i] = byte(r.Intn(256))
			}
		}
		return BinaryKey(b)
	}
	n := r.Intn(4)
	elems := make([]Key, n)
	for i := range elems {
		elems[i] = randKey(r, depth+1)
	}
	return Key{Kind: KeyArray, Arr: elems}
}

func randNum(r *rand.Rand) float64 {
	switch r.Intn(12) {
	case 0:
		return 0
	case 1:
		return math.Copysign(0, -1)
	case 2:
		return math.Inf(1)
	case 3:
		return math.Inf(-1)
	case 4:
		return math.SmallestNonzeroFloat64 * float64(r.Intn(5)-2)
	case 5:
		return float64(r.Intn(21) - 10)
	case 6:
		return float64(r.Int63n(1<<53)) * []float64{1, -1}[r.Intn(2)]
	case 7:
		return math.MaxFloat64 * []float64{1, -1}[r.Intn(2)]
	case 8:
		return float64(r.Intn(5)) + 0.5
	}
	return math.Float64frombits(r.Uint64()&^(0x7FF<<52) | uint64(r.Intn(0x7FE))<<52) // finite
}

var unitPool = []uint16{0, 1, 2, 3, 'a', 'b', 'z', 0x7F, 0x80, 0x7FF, 0x800, 0xD7FF, 0xD800, 0xDBFF, 0xDC00, 0xDFFF, 0xE000, 0xFFFF, 0x20AC}

func randUnits(r *rand.Rand) []uint16 {
	n := r.Intn(5)
	if r.Intn(10) == 0 {
		n += 20
	}
	u := make([]uint16, n)
	for i := range u {
		if r.Intn(3) == 0 {
			u[i] = uint16(r.Intn(0x10000))
		} else {
			u[i] = unitPool[r.Intn(len(unitPool))]
		}
	}
	return u
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// refWire is an independent transcription of encodeKey in idb_prelude.js (kept
// deliberately naive and separate from keys.go).
func refWire(k Key) string {
	var sb strings.Builder
	var units func(u []uint16)
	units = func(u []uint16) {
		for _, c := range u {
			switch {
			case c < 3:
				sb.WriteRune(2)
				sb.WriteRune(rune(c + 1))
			case c < 0x80:
				sb.WriteRune(rune(c))
			case c < 0x800:
				sb.WriteRune(rune(0xc0 | (c >> 6)))
				sb.WriteRune(rune(0x80 | (c & 63)))
			default:
				sb.WriteRune(rune(0xe0 | (c >> 12)))
				sb.WriteRune(rune(0x80 | ((c >> 6) & 63)))
				sb.WriteRune(rune(0x80 | (c & 63)))
			}
		}
	}
	var enc func(k Key)
	enc = func(k Key) {
		switch k.Kind {
		case KeyNumber, KeyDate:
			sb.WriteRune(rune(k.Kind))
			sb.WriteString(fmt.Sprintf("%016x", sortableBits(k.Num)))
		case KeyString:
			sb.WriteRune(0x30)
			units(k.Str)
			sb.WriteRune(1)
		case KeyBinary:
			sb.WriteRune(0x40)
			for _, c := range k.Bin {
				if c < 3 {
					sb.WriteRune(2)
					sb.WriteRune(rune(c + 1))
				} else {
					sb.WriteRune(rune(c))
				}
			}
			sb.WriteRune(1)
		case KeyArray:
			sb.WriteRune(0x50)
			for _, e := range k.Arr {
				enc(e)
			}
			sb.WriteRune(1)
		}
	}
	enc(k)
	return sb.String()
}

func TestKeyOrderMatchesReferenceComparator(t *testing.T) {
	r := rand.New(rand.NewSource(20260101))
	const n = 3000
	keys := make([]Key, n)
	st := make([][]byte, n)
	wire := make([]string, n)
	for i := range keys {
		keys[i] = randKey(r, 0)
		st[i] = keys[i].Storage()
		wire[i] = keys[i].Wire()
		if wire[i] != refWire(keys[i]) {
			t.Fatalf("wire form of %+v: got %q want %q", keys[i], wire[i], refWire(keys[i]))
		}
		w2, err := WireToStorage(refWire(keys[i]))
		if err != nil || !bytes.Equal(w2, st[i]) {
			t.Fatalf("transcode of %+v: %x vs %x (%v)", keys[i], w2, st[i], err)
		}
	}
	// all pairs among a sample, plus every key against its neighbours in reference order
	for i := 0; i < n; i++ {
		for j := 0; j < 120; j++ {
			b := r.Intn(n)
			ref := sign(CompareKeys(keys[i], keys[b]))
			if got := sign(bytes.Compare(st[i], st[b])); got != ref {
				t.Fatalf("storage order %+v vs %+v: ref %d got %d (%x %x)", keys[i], keys[b], ref, got, st[i], st[b])
			}
			if got := sign(strings.Compare(wire[i], wire[b])); got != ref {
				t.Fatalf("wire order %+v vs %+v: ref %d got %d", keys[i], keys[b], ref, got)
			}
		}
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return CompareKeys(keys[idx[a]], keys[idx[b]]) < 0 })
	for p := 1; p < n; p++ {
		a, b := idx[p-1], idx[p]
		c := bytes.Compare(st[a], st[b])
		if c > 0 || (c == 0 && CompareKeys(keys[a], keys[b]) != 0) {
			t.Fatalf("sorted neighbours disagree: %+v %+v", keys[a], keys[b])
		}
	}
}

func TestKeyRoundTripAndPrefixFree(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	var all [][]byte
	for i := 0; i < 1500; i++ {
		k := randKey(r, 0)
		s := k.Storage()
		got, n, err := DecodeStorageKey(s)
		if err != nil || n != len(s) || CompareKeys(got, k) != 0 {
			t.Fatalf("round trip %+v: got %+v n=%d/%d err=%v", k, got, n, len(s), err)
		}
		if !bytes.Equal(got.Storage(), s) {
			t.Fatalf("re-encoding differs for %+v", k)
		}
		w, err := StorageToWire(s)
		if err != nil || w != k.Wire() {
			t.Fatalf("wire of %+v: %q %v", k, w, err)
		}
		back, err := ParseWire(w)
		if err != nil || CompareKeys(back, k) != 0 {
			t.Fatalf("ParseWire(%q) = %+v %v", w, back, err)
		}
		all = append(all, s)
	}
	// no encoding is a proper prefix of a different one: required for index||primary key concatenation
	sort.Slice(all, func(i, j int) bool { return bytes.Compare(all[i], all[j]) < 0 })
	for i := 1; i < len(all); i++ {
		if !bytes.Equal(all[i-1], all[i]) && bytes.HasPrefix(all[i], all[i-1]) {
			t.Fatalf("%x is a prefix of %x", all[i-1], all[i])
		}
	}
}

// index entries are index-key || primary-key: their byte order must be (index key, primary key).
func TestCompositeIndexEntryOrder(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	type pair struct{ ik, pk Key }
	var ps []pair
	for i := 0; i < 600; i++ {
		ps = append(ps, pair{randKey(r, 1), randKey(r, 1)})
	}
	enc := func(p pair) []byte { return append(p.ik.Storage(), p.pk.Storage()...) }
	for i := 0; i < len(ps); i++ {
		for j := 0; j < 200; j++ {
			b := r.Intn(len(ps))
			want := CompareKeys(ps[i].ik, ps[b].ik)
			if want == 0 {
				want = CompareKeys(ps[i].pk, ps[b].pk)
			}
			if got := bytes.Compare(enc(ps[i]), enc(ps[b])); sign(got) != sign(want) {
				t.Fatalf("composite %+v vs %+v: want %d got %d", ps[i], ps[b], want, got)
			}
		}
	}
}

func TestKeySpecRules(t *testing.T) {
	// -0 equals 0
	if !bytes.Equal(NumberKey(math.Copysign(0, -1)).Storage(), NumberKey(0).Storage()) {
		t.Fatal("-0 and 0 must encode alike")
	}
	// type order: number < date < string < binary < array
	order := []Key{NumberKey(math.Inf(1)), DateKey(math.Inf(-1)), StringKey(""), BinaryKey(nil), ArrayKey()}
	for i := 1; i < len(order); i++ {
		if bytes.Compare(order[i-1].Storage(), order[i].Storage()) >= 0 {
			t.Fatalf("type order broken at %d", i)
		}
	}
	// strings compare by UTF-16 code unit: U+FFFF (one unit) sorts after U+1F600 (surrogates D83D DE00)
	if bytes.Compare(StringKey("\U0001F600").Storage(), StringKey("￿").Storage()) >= 0 {
		t.Fatal("string order must follow UTF-16 code units")
	}
	// arrays: prefix first, then element by element
	a, b := ArrayKey(NumberKey(1)), ArrayKey(NumberKey(1), NumberKey(0))
	if bytes.Compare(a.Storage(), b.Storage()) >= 0 {
		t.Fatal("array prefix must sort first")
	}
	if NumberKey(math.NaN()).Valid() || DateKey(math.NaN()).Valid() || ArrayKey(NumberKey(math.NaN())).Valid() {
		t.Fatal("NaN is not a key")
	}
	// the existing helpers agree with the wire form
	if EncodeNumberKey(3.5) != NumberKey(3.5).Wire() {
		t.Fatalf("EncodeNumberKey %q vs %q", EncodeNumberKey(3.5), NumberKey(3.5).Wire())
	}
	if _, err := WireToStorage("\x10zz"); err == nil {
		t.Fatal("garbage must be rejected")
	}
	if _, err := WireToStorage("\x30abc"); err == nil { // no terminator
		t.Fatal("unterminated string must be rejected")
	}
}

func FuzzWireToStorage(f *testing.F) {
	f.Add(NumberKey(1).Wire())
	f.Add(StringKey("a\x00b").Wire())
	f.Add(ArrayKey(StringKey("x"), BinaryKey([]byte{0, 1, 2})).Wire())
	f.Fuzz(func(t *testing.T, w string) {
		s, err := WireToStorage(w)
		if err != nil {
			return
		}
		back, err := StorageToWire(s)
		if err != nil {
			t.Fatalf("storage form %x of %q does not convert back: %v", s, w, err)
		}
		s2, err := WireToStorage(back)
		if err != nil || !bytes.Equal(s, s2) {
			t.Fatalf("not stable: %q -> %x -> %q -> %x", w, s, back, s2)
		}
	})
}
