package vp8enc

// Macroblock-level types shared by the analysis and the bitstream writer.

// Reference frames.
const (
	refIntra  = 0
	refLast   = 1
	refGolden = 2
	refAlt    = 3
)

// Macroblock modes (numbering follows the decoder).
const (
	mbDC = iota
	mbV
	mbH
	mbTM
	mbB
	mbNearest
	mbNear
	mbZero
	mbNew
	mbSplit
)

// mv is a motion vector in eighth-pel units (luma vectors are always even: quarter-pel precision).
type mv struct{ r, c int16 }

// mbInfo is what neighbouring macroblocks need to know to predict motion vectors and contexts.
type mbInfo struct {
	ref  uint8
	mode uint8
	mv   mv
}

// mbOut is everything the first partition needs about one macroblock.
type mbOut struct {
	ref    uint8
	mode   uint8 // mbDC..mbTM for intra, mbNearest..mbNew for inter
	uvmode uint8
	skip   bool
	mv     mv
	cnt    [4]uint8 // neighbour counts selecting the mode probabilities
	best   mv       // base of a NEW vector
}

// findNearMVs reproduces the decoder's neighbour scan (RFC 6386 16.3): it returns the
// candidate vectors (best, nearest, near) and the counts selecting the mode probabilities.
// infos has a border row above and a border column to the left of the picture
// (intra, zero vector); idx is the entry of the macroblock itself.
func findNearMVs(infos []mbInfo, idx, stride, mbx, mby, mbw, mbh int) (best, nearest, near mv, cnt [4]int) {
	above, left, al := &infos[idx-stride], &infos[idx-1], &infos[idx-stride-1]
	var nm [4]mv
	n := 0
	if above.ref != refIntra {
		if above.mv != (mv{}) {
			n++
			nm[n] = above.mv
			cnt[n] += 2
		} else {
			cnt[0] += 2
		}
	}
	if left.ref != refIntra {
		if left.mv != (mv{}) {
			t := left.mv
			if t != nm[n] {
				n++
				nm[n] = t
			}
			cnt[n] += 2
		} else {
			cnt[0] += 2
		}
	}
	if al.ref != refIntra {
		if al.mv != (mv{}) {
			t := al.mv
			if t != nm[n] {
				n++
				nm[n] = t
			}
			cnt[n]++
		} else {
			cnt[0]++
		}
	}
	if cnt[3] > 0 && nm[n] == nm[1] {
		cnt[1]++
	}
	cnt[3] = 0
	if above.mode == mbSplit {
		cnt[3] += 2
	}
	if left.mode == mbSplit {
		cnt[3] += 2
	}
	if al.mode == mbSplit {
		cnt[3]++
	}
	if cnt[2] > cnt[1] {
		cnt[1], cnt[2] = cnt[2], cnt[1]
		nm[1], nm[2] = nm[2], nm[1]
	}
	if cnt[1] >= cnt[0] {
		nm[0] = nm[1]
	}
	best = clampMV(nm[0], mbx, mby, mbw, mbh)
	nearest = clampMV(nm[1], mbx, mby, mbw, mbh)
	near = clampMV(nm[2], mbx, mby, mbw, mbh)
	return
}

func clampMV(m mv, mbx, mby, mbw, mbh int) mv {
	toLeft := -(mbx*16<<3 + 128)
	toRight := ((mbw-1-mbx)*16)<<3 + 128
	toTop := -(mby*16<<3 + 128)
	toBottom := ((mbh-1-mby)*16)<<3 + 128
	c, r := int(m.c), int(m.r)
	if c < toLeft {
		c = toLeft
	} else if c > toRight {
		c = toRight
	}
	if r < toTop {
		r = toTop
	} else if r > toBottom {
		r = toBottom
	}
	return mv{int16(r), int16(c)}
}
