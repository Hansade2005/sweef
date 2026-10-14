package vp8enc

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/media/vp8"
)

// DCT token alphabet (RFC 6386 13.2).
const (
	tokZero = iota
	tokOne
	tokTwo
	tokThree
	tokFour
	tokCat1
	tokCat2
	tokCat3
	tokCat4
	tokCat5
	tokCat6
	tokEOB
)

// Block planes of the token probability table.
const (
	planeY1WithY2 = 0
	planeY2       = 1
	planeUV       = 2
	planeY1NoY2   = 3
)

type pathStep struct {
	node uint8
	bit  bool
}

// tokPath lists the branch decisions of the token tree below the end-of-block
// decision (node 0), indexed by token.
var tokPath = [tokEOB][]pathStep{
	tokZero:  {{1, false}},
	tokOne:   {{1, true}, {2, false}},
	tokTwo:   {{1, true}, {2, true}, {3, false}, {4, false}},
	tokThree: {{1, true}, {2, true}, {3, false}, {4, true}, {5, false}},
	tokFour:  {{1, true}, {2, true}, {3, false}, {4, true}, {5, true}},
	tokCat1:  {{1, true}, {2, true}, {3, true}, {6, false}, {7, false}},
	tokCat2:  {{1, true}, {2, true}, {3, true}, {6, false}, {7, true}},
	tokCat3:  {{1, true}, {2, true}, {3, true}, {6, true}, {8, false}, {9, false}},
	tokCat4:  {{1, true}, {2, true}, {3, true}, {6, true}, {8, false}, {9, true}},
	tokCat5:  {{1, true}, {2, true}, {3, true}, {6, true}, {8, true}, {10, false}},
	tokCat6:  {{1, true}, {2, true}, {3, true}, {6, true}, {8, true}, {10, true}},
}

// Extra-bit probabilities and bases of the value categories.
var (
	catBase  = [...]int{5, 7, 11, 19, 35, 67}
	catProbs = [...][]uint8{
		{159},
		{165, 145},
		{173, 148, 140},
		{176, 155, 140, 135},
		{180, 157, 141, 134, 130},
		{254, 254, 243, 230, 196, 177, 153, 140, 133, 130, 129},
	}
)

// A token entry packs everything needed to write (and count) one token.
//
//	bits 0-3 token, 4 the previous token was ZERO (no end-of-block decision), 5-6 plane, 7-9 band,
//	10-11 context, 12 sign, 13-23 extra bits.
type tokEntry uint32

func mkTok(tok, skipEOB, plane, band, ctx, sign, extra int) tokEntry {
	return tokEntry(tok | skipEOB<<4 | plane<<5 | band<<7 | ctx<<10 | sign<<12 | extra<<13)
}

// tokenOf classifies a level magnitude (>= 0) and returns the token and its extra-bit value.
func tokenOf(a int) (tok, extra int) {
	switch {
	case a <= 4:
		return a, 0 // ZERO..FOUR share the numbering
	case a <= 6:
		return tokCat1, a - 5
	case a <= 10:
		return tokCat2, a - 7
	case a <= 18:
		return tokCat3, a - 11
	case a <= 34:
		return tokCat4, a - 19
	case a <= 66:
		return tokCat5, a - 35
	default:
		if a > 67+2047 {
			a = 67 + 2047
		}
		return tokCat6, a - 67
	}
}

// tokenCounts counts the branch decisions: [plane][band][context][node][bit].
type tokenCounts [4][8][3][11][2]uint32

func (c *tokenCounts) add(o *tokenCounts) {
	for i := range c {
		for j := range c[i] {
			for k := range c[i][j] {
				for l := range c[i][j][k] {
					c[i][j][k][l][0] += o[i][j][k][l][0]
					c[i][j][k][l][1] += o[i][j][k][l][1]
				}
			}
		}
	}
}

// emitBlock appends the tokens of one block to toks, counts them and returns whether the block has coefficients.
// lv holds the levels in coded (zigzag) order; first is 1 for luma blocks whose DC travels in the Y2 block;
// eob is the index after the last non-zero level.
func emitBlock(toks []tokEntry, cnt *tokenCounts, plane, ctx int, lv *[16]int16, first, eob int) ([]tokEntry, bool) {
	if eob <= first {
		band := int(bands[first])
		cnt[plane][band][ctx][0][0]++
		return append(toks, mkTok(tokEOB, 0, plane, band, ctx, 0, 0)), false
	}
	skip := 0
	for i := first; i < eob; i++ {
		band := int(bands[i])
		v := int(lv[i])
		sign := 0
		if v < 0 {
			v, sign = -v, 1
		}
		tok, extra := tokenOf(v)
		toks = append(toks, mkTok(tok, skip, plane, band, ctx, sign, extra))
		c := &cnt[plane][band][ctx]
		if skip == 0 {
			c[0][1]++
		}
		for _, st := range tokPath[tok] {
			if st.bit {
				c[st.node][1]++
			} else {
				c[st.node][0]++
			}
		}
		switch {
		case v == 0:
			ctx, skip = 0, 1
		case v == 1:
			ctx, skip = 1, 0
		default:
			ctx, skip = 2, 0
		}
	}
	if eob < 16 {
		band := int(bands[eob])
		cnt[plane][band][ctx][0][0]++
		toks = append(toks, mkTok(tokEOB, 0, plane, band, ctx, 0, 0))
	}
	return toks, true
}

// writeTokens writes the token entries with the given probabilities.
func writeTokens(e *boolEncoder, probs *vp8.CoeffProbs, toks []tokEntry) {
	for _, t := range toks {
		tok := int(t & 15)
		skip := t>>4&1 != 0
		p := &probs[t>>5&3][t>>7&7][t>>10&3]
		if tok == tokEOB {
			e.put(p[0], false)
			continue
		}
		if !skip {
			e.put(p[0], true)
		}
		for _, st := range tokPath[tok] {
			e.put(p[st.node], st.bit)
		}
		if tok == tokZero {
			continue // no sign for a zero
		}
		if tok >= tokCat1 {
			cat := tok - tokCat1
			extra := int(t >> 13 & 0x7ff)
			probsX := catProbs[cat]
			for i, pr := range probsX {
				e.put(pr, extra>>uint(len(probsX)-1-i)&1 != 0)
			}
		}
		e.put(128, t>>12&1 != 0)
	}
}

// ---- bit cost model ---------------------------------------------------------------------

// bitCost[p] is the cost in 1/256 bit of coding a 0 with probability p/256.
var bitCost = func() (t [256]uint16) {
	for p := 1; p < 256; p++ {
		t[p] = uint16(math.Round(-math.Log2(float64(p)/256) * 256))
	}
	t[0] = t[1]
	return
}()

func cost(prob uint8, bit bool) int {
	if bit {
		return int(bitCost[256-int(prob)])
	}
	return int(bitCost[prob])
}

// chooseCoeffProbs decides the token probability updates of a frame: for every
// context it computes the probability that minimises the cost of the counted
// decisions and keeps it when the saving pays for the update (8 bits plus the
// change of the update flag). It returns the probabilities in force for the frame
// and which of them are signalled.
func chooseCoeffProbs(cur vp8.CoeffProbs, cnt *tokenCounts, upd *vp8.CoeffProbs) (next vp8.CoeffProbs, changed *[4][8][3][11]bool, n int) {
	next = cur
	ch := new([4][8][3][11]bool)
	for i := range cnt {
		for j := range cnt[i] {
			for k := range cnt[i][j] {
				for l := range cnt[i][j][k] {
					c0, c1 := int(cnt[i][j][k][l][0]), int(cnt[i][j][k][l][1])
					if c0+c1 == 0 {
						continue
					}
					old := cur[i][j][k][l]
					np := (c0*256 + (c0+c1)/2) / (c0 + c1)
					if np < 1 {
						np = 1
					} else if np > 255 {
						np = 255
					}
					if uint8(np) == old {
						continue
					}
					oldBits := c0*int(bitCost[old]) + c1*int(bitCost[256-int(old)])
					newBits := c0*int(bitCost[np]) + c1*int(bitCost[256-np])
					u := upd[i][j][k][l]
					updCost := 8*256 + int(bitCost[256-int(u)]) - int(bitCost[u])
					if oldBits-newBits > updCost {
						next[i][j][k][l] = uint8(np)
						ch[i][j][k][l] = true
						n++
					}
				}
			}
		}
	}
	return next, ch, n
}
