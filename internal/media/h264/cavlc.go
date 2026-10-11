package h264

import "sync"

import "math/bits"

// CAVLC residual block decoding (9.2).

type vlcEntry uint16 // len<<8 | value

type vlcTable struct {
	bits uint
	t    []vlcEntry
}

func buildVLC(lens, codes []uint8, maxBits uint) *vlcTable {
	t := &vlcTable{bits: maxBits, t: make([]vlcEntry, 1<<maxBits)}
	for sym := range lens {
		l := uint(lens[sym])
		if l == 0 {
			continue
		}
		code := uint32(codes[sym]) << (maxBits - l)
		for k := uint32(0); k < 1<<(maxBits-l); k++ {
			t.t[code+k] = vlcEntry(l<<8 | uint(sym))
		}
	}
	return t
}

var (
	coeffTokenVLC       [3]*vlcTable
	chromaDCTokenVLC    *vlcTable
	chroma422DCTokenVLC *vlcTable
	chroma422DCZerosVLC [8]*vlcTable
	totalZerosVLC       [16]*vlcTable
	chromaDCZerosVLC    [4]*vlcTable
	runBeforeVLC        [8]*vlcTable
)

var vlcOnce sync.Once

// initVLC builds the CAVLC tables on first decoder use rather than at process start.
func initVLC() {
	for k := 0; k < 3; k++ {
		maxLen := uint(0)
		for _, l := range coeffTokenLen[k] {
			if uint(l) > maxLen {
				maxLen = uint(l)
			}
		}
		coeffTokenVLC[k] = buildVLC(coeffTokenLen[k][:], coeffTokenBits[k][:], maxLen)
	}
	chromaDCTokenVLC = buildVLC(chromaDCCoeffTokenLen[:], chromaDCCoeffTokenBits[:], 8)
	for tz := 1; tz < 16; tz++ {
		totalZerosVLC[tz] = buildVLC(totalZerosLen[tz-1][:], totalZerosBits[tz-1][:], 9)
	}
	for tz := 1; tz < 4; tz++ {
		chromaDCZerosVLC[tz] = buildVLC(chromaDCTotalZerosLen[tz-1][:], chromaDCTotalZerosBits[tz-1][:], 3)
	}
	chroma422DCTokenVLC = buildVLC(chroma422DCCoeffTokenLen[:], chroma422DCCoeffTokenBits[:], 13)
	for tz := 1; tz < 8; tz++ {
		chroma422DCZerosVLC[tz] = buildVLC(chroma422DCTotalZerosLen[tz-1][:], chroma422DCTotalZerosBits[tz-1][:], 5)
	}
	for z := 1; z < 8; z++ {
		runBeforeVLC[z] = buildVLC(runLen[z-1][:], runBits[z-1][:], 11)
	}
}

func (t *vlcTable) read(r *bitReader) (int, bool) {
	e := t.t[r.peek(t.bits)]
	l := uint(e >> 8)
	if l == 0 {
		return 0, false
	}
	r.pos += int(l)
	return int(e & 0xff), true
}

// cavlcBlock decodes one residual block. nC selects the coeff_token table
// (-1 for chroma DC). Levels are written to lv in scan order [0,maxNum).
// It returns TotalCoeff, or -1 on a bitstream error.
func (d *Decoder) cavlcBlock(nC, maxNum int, lv *[64]int32) int {
	r := d.br
	for i := 0; i < maxNum; i++ {
		lv[i] = 0
	}
	var sym int
	var ok bool
	switch {
	case nC == -2:
		sym, ok = chroma422DCTokenVLC.read(r)
	case nC == -1:
		sym, ok = chromaDCTokenVLC.read(r)
	case nC < 2:
		sym, ok = coeffTokenVLC[0].read(r)
	case nC < 4:
		sym, ok = coeffTokenVLC[1].read(r)
	case nC < 8:
		sym, ok = coeffTokenVLC[2].read(r)
	default:
		v := int(r.u(6))
		// 6-bit FLC: xxxxyy; 000011 -> 0 coeffs
		if v == 3 {
			sym = 0
		} else {
			tc := v>>2 + 1
			sym = tc*4 + v&3
		}
		ok = true
	}
	if !ok {
		return -1
	}
	total, t1 := sym>>2, sym&3
	if total == 0 {
		return 0
	}
	if total > maxNum {
		return -1
	}
	var level [16]int32
	suffixLen := 0
	if total > 10 && t1 < 3 {
		suffixLen = 1
	}
	for i := 0; i < total; i++ {
		if i < t1 {
			level[i] = 1 - 2*int32(r.u1())
			continue
		}
		// level_prefix
		w := r.peek(32)
		if w == 0 {
			return -1
		}
		prefix := bits.LeadingZeros32(w)
		r.pos += prefix + 1
		levelCode := min(15, prefix) << uint(suffixLen)
		if suffixLen > 0 || prefix >= 14 {
			size := suffixLen
			if prefix == 14 && suffixLen == 0 {
				size = 4
			} else if prefix >= 15 {
				size = prefix - 3
			}
			if size > 0 {
				levelCode += int(r.u(uint(size)))
			}
		}
		if prefix >= 15 && suffixLen == 0 {
			levelCode += 15
		}
		if prefix >= 16 {
			levelCode += (1 << uint(prefix-3)) - 4096
		}
		if i == t1 && t1 < 3 {
			levelCode += 2
		}
		var lvl int
		if levelCode&1 == 0 {
			lvl = (levelCode + 2) >> 1
		} else {
			lvl = (-levelCode - 1) >> 1
		}
		level[i] = int32(lvl)
		if suffixLen == 0 {
			suffixLen = 1
		}
		a := lvl
		if a < 0 {
			a = -a
		}
		if a > (3<<uint(suffixLen-1)) && suffixLen < 6 {
			suffixLen++
		}
	}
	zerosLeft := 0
	if total < maxNum {
		if nC == -2 {
			if total > 7 {
				return -1
			}
			z, ok := chroma422DCZerosVLC[total].read(r)
			if !ok {
				return -1
			}
			zerosLeft = z
		} else if nC == -1 {
			if total > 3 {
				return -1
			}
			z, ok := chromaDCZerosVLC[total].read(r)
			if !ok {
				return -1
			}
			zerosLeft = z
		} else {
			z, ok := totalZerosVLC[total].read(r)
			if !ok {
				return -1
			}
			zerosLeft = z
		}
	}
	// place coefficients: level[0] is the highest-frequency one
	pos := total + zerosLeft - 1
	if pos >= maxNum {
		return -1
	}
	for i := 0; i < total; i++ {
		lv[pos] = level[i]
		if i == total-1 {
			break
		}
		run := 0
		if zerosLeft > 0 {
			k := zerosLeft
			if k > 7 {
				k = 7
			}
			run2, ok := runBeforeVLC[k].read(r)
			if !ok {
				return -1
			}
			run = run2
			zerosLeft -= run
			if zerosLeft < 0 {
				return -1
			}
		}
		pos -= run + 1
	}
	return total
}
