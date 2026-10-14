package vp8enc

import (
	"math/rand"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/vp8"
)

// decodeBlock is a transcription of the decoder's residual parser on top of the reference bool decoder.
func decodeBlock(r *boolDecoder, probs *vp8.CoeffProbs, plane, ctx, first int) (lv [16]int16, ok bool) {
	prob := &probs[plane]
	n := first
	p := prob[bands[n]][ctx]
	if !r.get(p[0]) {
		return lv, false
	}
	for n != 16 {
		n++
		if !r.get(p[1]) {
			p = prob[bands[n]][0]
			continue
		}
		var v int
		if !r.get(p[2]) {
			v = 1
			p = prob[bands[n]][1]
		} else {
			if !r.get(p[3]) {
				if !r.get(p[4]) {
					v = 2
				} else {
					v = 3 + b2i(r.get(p[5]))
				}
			} else if !r.get(p[6]) {
				if !r.get(p[7]) {
					v = 5 + b2i(r.get(159))
				} else {
					v = 7 + 2*b2i(r.get(165)) + b2i(r.get(145))
				}
			} else {
				b1 := b2i(r.get(p[8]))
				b0 := b2i(r.get(p[9+b1]))
				cat := 2*b1 + b0
				tab := catProbs[2+cat]
				v = 0
				for _, pr := range tab {
					v = v*2 + b2i(r.get(pr))
				}
				v += 3 + (8 << uint(cat))
			}
			p = prob[bands[n]][2]
		}
		c := v
		if r.get(128) {
			c = -c
		}
		lv[n-1] = int16(c)
		if n == 16 || !r.get(p[0]) {
			return lv, true
		}
	}
	return lv, true
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestTokenRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	probs := vp8.DefaultCoeffProbs()
	for trial := 0; trial < 300; trial++ {
		type blk struct {
			lv         [16]int16
			plane, ctx int
			first, eob int
		}
		var blocks []blk
		var toks []tokEntry
		var cnt tokenCounts
		for b := 0; b < 20; b++ {
			var x blk
			x.plane = r.Intn(4)
			x.ctx = r.Intn(3)
			if x.plane == planeY1WithY2 {
				x.first = 1
			}
			for i := x.first; i < 16; i++ {
				switch r.Intn(4) {
				case 0:
					x.lv[i] = int16(r.Intn(5) - 2)
				case 1:
					x.lv[i] = int16(r.Intn(200) - 100)
				case 2:
					x.lv[i] = 0
				default:
					x.lv[i] = int16(r.Intn(2100) - 1050)
				}
				if r.Intn(5) == 0 {
					break
				}
			}
			for i := 15; i >= x.first; i-- {
				if x.lv[i] != 0 {
					x.eob = i + 1
					break
				}
			}
			toks, _ = emitBlock(toks, &cnt, x.plane, x.ctx, &x.lv, x.first, x.eob)
			blocks = append(blocks, x)
		}
		var e boolEncoder
		e.init(nil)
		writeTokens(&e, &probs, toks)
		out := e.finish()
		d := newBoolDecoder(out)
		for i, x := range blocks {
			lv, has := decodeBlock(d, &probs, x.plane, x.ctx, x.first)
			if has != (x.eob > x.first) || lv != x.lv {
				t.Fatalf("trial %d block %d: plane %d ctx %d first %d: want %v (eob %d) got %v (has %v)", trial, i, x.plane, x.ctx, x.first, x.lv, x.eob, lv, has)
			}
		}
	}
}
