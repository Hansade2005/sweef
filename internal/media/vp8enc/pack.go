package vp8enc

import (
	"image"
	"sync"

	"github.com/sweefbrowser/sweef/internal/media/vp8"
)

// imageOf views a reconstructed frame as an image.YCbCr over the picture area (padding excluded).
func imageOf(f *frame) *image.YCbCr {
	return &image.YCbCr{
		Y: f.y.pix[f.y.off:], Cb: f.u.pix[f.u.off:], Cr: f.v.pix[f.v.off:],
		YStride: f.y.stride, CStride: f.u.stride,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, f.y.w, f.y.h),
	}
}

func probOf(n0, total int) uint8 {
	if total <= 0 {
		return 128
	}
	p := (n0*256 + total/2) / total
	return uint8(clampI(p, 1, 255))
}

var (
	defaultYMode, defaultUVMode = vp8.DefaultIntraModeProbs()
	defaultMVProbs              = vp8.DefaultMVProbs()
	mvUpdateProbs               = vp8.MVUpdateProbs()
	modeContexts                = vp8.ModeContexts()
)

// pack writes the frame: tag, first partition (header and modes) and the token partitions.
func (e *Encoder) pack(key bool, cnt *tokenCounts, nIntra, nSkip, nNoSkip int, nRef [4]int) []byte {
	base := e.probs
	if key {
		base = e.defProbs
	}
	next, changed, _ := chooseCoeffProbs(base, cnt, &e.updProbs)
	e.nextProbs = next

	n := e.mbw * e.mbh
	probSkip := probOf(nNoSkip, nSkip+nNoSkip)
	nInter := nRef[refLast] + nRef[refGolden] + nRef[refAlt]
	probIntra := probOf(nIntra, n)
	probLast := probOf(nRef[refLast], nInter)
	probGF := probOf(nRef[refGolden], nRef[refGolden]+nRef[refAlt])

	e.refreshGolden = false
	if !key {
		sceneCut := n > 0 && nIntra*100/n > 45
		e.refreshGolden = sceneCut || e.sinceGolden >= int(e.cfg.FrameRate*5)
	}

	// token partitions in parallel
	np := 1 << e.log2parts
	var wg sync.WaitGroup
	for p := 0; p < np; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			var be boolEncoder
			be.init(e.partBufs[p])
			for r := p; r < e.mbh; r += np {
				writeTokens(&be, &next, e.rowToks[r])
			}
			e.partBufs[p] = be.finish()
		}(p)
	}

	var be boolEncoder
	be.init(e.firstBuf)
	if key {
		be.flag(false) // colour space
		be.flag(false) // clamping type
	}
	be.flag(false) // segmentation disabled
	be.flag(false) // normal loop filter
	be.literal(uint32(lfLevel(e.q, key)), 6)
	be.literal(0, 3) // sharpness
	be.flag(false)   // no loop filter deltas
	be.literal(uint32(e.log2parts), 2)
	be.literal(uint32(e.q), 7)
	for i := 0; i < 5; i++ {
		be.flag(false) // no quantiser deltas
	}
	if key {
		be.flag(true) // refresh entropy probs
	} else {
		be.flag(e.refreshGolden) // refresh golden
		be.flag(false)           // refresh altref
		if !e.refreshGolden {
			be.literal(0, 2) // copy to golden: none
		}
		if e.refreshGolden {
			be.literal(2, 2) // the old golden frame moves to the altref slot
		} else {
			be.literal(0, 2)
		}
		be.flag(false) // sign bias golden
		be.flag(false) // sign bias altref
		be.flag(true)  // refresh entropy probs
		be.flag(true)  // refresh last
	}
	for i := range changed {
		for j := range changed[i] {
			for k := range changed[i][j] {
				for l := range changed[i][j][k] {
					c := changed[i][j][k][l]
					be.put(e.updProbs[i][j][k][l], c)
					if c {
						be.literal(uint32(next[i][j][k][l]), 8)
					}
				}
			}
		}
	}
	be.flag(true) // mb_no_coeff_skip
	be.literal(uint32(probSkip), 8)
	if !key {
		be.literal(uint32(probIntra), 8)
		be.literal(uint32(probLast), 8)
		be.literal(uint32(probGF), 8)
		be.flag(false) // no luma intra mode probability update
		be.flag(false) // no chroma intra mode probability update
		for i := 0; i < 2; i++ {
			for j := 0; j < 19; j++ {
				be.put(mvUpdateProbs[i][j], false)
			}
		}
	}
	for i := 0; i < n; i++ {
		o := &e.mbOut[i]
		be.put(probSkip, o.skip)
		if key {
			writeKFModes(&be, o)
			continue
		}
		if o.ref == refIntra {
			be.put(probIntra, false)
			writeIntraModes(&be, o)
			continue
		}
		be.put(probIntra, true)
		be.put(probLast, o.ref != refLast)
		if o.ref != refLast {
			be.put(probGF, o.ref == refAlt)
		}
		mc := &modeContexts
		c := &o.cnt
		switch o.mode {
		case mbZero:
			be.put(mc[c[0]][0], false)
		case mbNearest:
			be.put(mc[c[0]][0], true)
			be.put(mc[c[1]][1], false)
		case mbNear:
			be.put(mc[c[0]][0], true)
			be.put(mc[c[1]][1], true)
			be.put(mc[c[2]][2], false)
		default: // mbNew
			be.put(mc[c[0]][0], true)
			be.put(mc[c[1]][1], true)
			be.put(mc[c[2]][2], true)
			be.put(mc[c[3]][3], false)
			writeMVComponent(&be, (int(o.mv.r)-int(o.best.r))/2, &defaultMVProbs[0])
			writeMVComponent(&be, (int(o.mv.c)-int(o.best.c))/2, &defaultMVProbs[1])
		}
	}
	first := be.finish()
	e.firstBuf = first[:0]
	wg.Wait()

	size := 3 + len(first) + 3*(np-1)
	if key {
		size += 7
	}
	for _, b := range e.partBufs {
		size += len(b)
	}
	out := make([]byte, 0, size)
	tag := uint32(len(first))<<5 | 1<<4
	if !key {
		tag |= 1
	}
	out = append(out, byte(tag), byte(tag>>8), byte(tag>>16))
	if key {
		w, h := e.cfg.Width, e.cfg.Height
		out = append(out, 0x9d, 0x01, 0x2a, byte(w), byte(w>>8), byte(h), byte(h>>8))
	}
	out = append(out, first...)
	for p := 0; p < np-1; p++ {
		l := len(e.partBufs[p])
		out = append(out, byte(l), byte(l>>8), byte(l>>16))
	}
	for _, b := range e.partBufs {
		out = append(out, b...)
	}
	return out
}

func writeKFModes(be *boolEncoder, o *mbOut) {
	be.put(145, true) // not B_PRED
	switch o.mode {
	case mbDC:
		be.put(156, false)
		be.put(163, false)
	case mbV:
		be.put(156, false)
		be.put(163, true)
	case mbH:
		be.put(156, true)
		be.put(128, false)
	default:
		be.put(156, true)
		be.put(128, true)
	}
	writeKFUV(be, o.uvmode)
}

func writeKFUV(be *boolEncoder, m uint8) {
	switch m {
	case mbDC:
		be.put(142, false)
	case mbV:
		be.put(142, true)
		be.put(114, false)
	case mbH:
		be.put(142, true)
		be.put(114, true)
		be.put(183, false)
	default:
		be.put(142, true)
		be.put(114, true)
		be.put(183, true)
	}
}

func writeIntraModes(be *boolEncoder, o *mbOut) {
	yp, up := &defaultYMode, &defaultUVMode
	switch o.mode {
	case mbDC:
		be.put(yp[0], false)
	case mbV:
		be.put(yp[0], true)
		be.put(yp[1], false)
		be.put(yp[2], false)
	case mbH:
		be.put(yp[0], true)
		be.put(yp[1], false)
		be.put(yp[2], true)
	default: // TM
		be.put(yp[0], true)
		be.put(yp[1], true)
		be.put(yp[3], false)
	}
	switch o.uvmode {
	case mbDC:
		be.put(up[0], false)
	case mbV:
		be.put(up[0], true)
		be.put(up[1], false)
	case mbH:
		be.put(up[0], true)
		be.put(up[1], true)
		be.put(up[2], false)
	default:
		be.put(up[0], true)
		be.put(up[1], true)
		be.put(up[2], true)
	}
}

// writeMVComponent codes one vector component in quarter-pel units (RFC 6386 17.2).
func writeMVComponent(be *boolEncoder, v int, p *[19]uint8) {
	x := v
	if x < 0 {
		x = -x
	}
	if x < 8 {
		be.put(p[0], false)
		sp := p[2:9]
		if x < 4 {
			be.put(sp[0], false)
			if x < 2 {
				be.put(sp[1], false)
				be.put(sp[2], x == 1)
			} else {
				be.put(sp[1], true)
				be.put(sp[3], x == 3)
			}
		} else {
			be.put(sp[0], true)
			if x < 6 {
				be.put(sp[4], false)
				be.put(sp[5], x == 5)
			} else {
				be.put(sp[4], true)
				be.put(sp[6], x == 7)
			}
		}
	} else {
		be.put(p[0], true)
		for i := 0; i < 3; i++ {
			be.put(p[9+i], x>>uint(i)&1 != 0)
		}
		for i := 9; i > 3; i-- {
			be.put(p[9+i], x>>uint(i)&1 != 0)
		}
		if x&0xfff0 != 0 {
			be.put(p[9+3], x>>3&1 != 0)
		}
	}
	if x != 0 {
		be.put(p[1], v < 0)
	}
}
