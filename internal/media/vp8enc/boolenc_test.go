package vp8enc

import (
	"math/rand"
	"testing"
)

// boolDecoder is the reference decoder of RFC 6386 section 7.3 (test only).
type boolDecoder struct {
	in    []byte
	pos   int
	value uint32
	rng   uint32
	bits  int
}

func newBoolDecoder(b []byte) *boolDecoder {
	d := &boolDecoder{in: b, rng: 255}
	for i := 0; i < 2; i++ {
		d.value = d.value<<8 | uint32(d.next())
	}
	return d
}

func (d *boolDecoder) next() byte {
	if d.pos < len(d.in) {
		b := d.in[d.pos]
		d.pos++
		return b
	}
	return 0
}

func (d *boolDecoder) get(prob uint8) bool {
	split := 1 + (((d.rng - 1) * uint32(prob)) >> 8)
	bigSplit := split << 8
	var ret bool
	if d.value >= bigSplit {
		ret = true
		d.rng -= split
		d.value -= bigSplit
	} else {
		d.rng = split
	}
	for d.rng < 128 {
		d.value <<= 1
		d.rng <<= 1
		d.bits++
		if d.bits == 8 {
			d.bits = 0
			d.value |= uint32(d.next())
		}
	}
	return ret
}

func TestBoolEncoderRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for trial := 0; trial < 50; trial++ {
		n := 1 + r.Intn(5000)
		probs := make([]uint8, n)
		bits := make([]bool, n)
		for i := range probs {
			probs[i] = uint8(1 + r.Intn(255))
			bits[i] = r.Intn(256) >= int(probs[i])
		}
		var e boolEncoder
		e.init(nil)
		for i := range bits {
			e.put(probs[i], bits[i])
		}
		out := e.finish()
		d := newBoolDecoder(out)
		for i := range bits {
			if d.get(probs[i]) != bits[i] {
				t.Fatalf("trial %d: bit %d differs", trial, i)
			}
		}
	}
}
