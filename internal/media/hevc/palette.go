package hevc

// Palette mode (screen content coding extension, 7.3.8.13 and 8.4.4.2.7).
//
// A palette coded CU carries a palette (partly predicted from the palette
// predictor that is kept across CUs), a map of palette indices coded with
// index runs and copy-above runs along a traverse scan, and optionally
// escape samples that are coded explicitly.

const (
	palRunLeft  = 0 // index run (copy-left)
	palRunAbove = 1 // copy-above run
)

// palPredictor is the palette predictor (PredictorPaletteEntries).
type palPredictor struct {
	size int
	ent  [3][128]uint16
}

// initPalPred resets the predictor to the initializers of the PPS, the SPS or
// to empty.
func (d *Decoder) initPalPred() {
	s, p := d.sp, d.pp
	d.palPred.size = 0
	if s == nil || !s.paletteMode {
		return
	}
	var src *[3][]uint16
	switch {
	case p != nil && p.palettePredPres:
		src = &p.palettePredInit
	case len(s.palettePredInit[0]) > 0:
		src = &s.palettePredInit
	default:
		return
	}
	n := min(len(src[0]), s.palettePredMax)
	d.palPred.size = n
	nc := 3
	if s.chromaFormat == 0 {
		nc = 1
	}
	for c := 0; c < nc; c++ {
		copy(d.palPred.ent[c][:n], src[c])
	}
}

// egk reads a k-th order Exp-Golomb code from bypass bins.
func (c *cabac) egk(k int) int {
	v := 0
	for {
		if c.bypass() == 0 {
			break
		}
		v += 1 << uint(k)
		k++
		if k > 30 {
			return v
		}
	}
	if k > 0 {
		v += c.bypassBits(k)
	}
	return v
}

// truncBin reads a truncated binary code for symbols 0..maxSymbol-1.
func (c *cabac) truncBin(maxSymbol int) int {
	if maxSymbol <= 1 {
		return 0
	}
	k := 0
	for 1<<uint(k+1) <= maxSymbol {
		k++
	}
	val := 1 << uint(k)
	u := val - (maxSymbol - val)
	v := c.bypassBits(k)
	if v >= u {
		v = v<<1 | c.bypass()
		v -= u
	}
	return v
}

// coeffRemain reads a coeff_abs_level_remaining style code with the given
// Rice parameter (without the extended precision escape).
func (c *cabac) coeffRemain(rice int) (int, bool) {
	prefix := 0
	for prefix < 32 && c.bypass() != 0 {
		prefix++
	}
	if prefix <= 3 {
		return prefix<<uint(rice) + c.bypassBits(rice), true
	}
	sl := prefix - 3 + rice
	if sl > 30 {
		return 0, false
	}
	return (((1 << uint(prefix-3)) + 3 - 1) << uint(rice)) + c.bypassBits(sl), true
}

func bitLen(v int) int {
	n := 0
	for v > 0 {
		n++
		v >>= 1
	}
	return n
}

// palRun reads a run length with the most-significant-bit coding of the
// palette run syntax elements.
func (c *cabac) palRun(copyAbove bool, idx, maxRun int) int {
	if maxRun <= 0 {
		return 0
	}
	var lut [5]int
	base := ctxPalLeftRun
	if copyAbove {
		lut = [5]int{0, 1, 1, 2, 2}
		base = ctxPalTopRun
	} else {
		first := 2
		if idx < 1 {
			first = 0
		} else if idx < 3 {
			first = 1
		}
		lut = [5]int{first, 3, 3, 4, 4}
	}
	numBins := bitLen(maxRun)
	// truncated unary prefix: the first five bins are context coded
	i := 0
	var bin int
	for {
		if i > 4 {
			bin = c.bypass()
		} else {
			bin = c.bin(base + lut[i])
		}
		i++
		if bin == 0 || i >= numBins {
			break
		}
	}
	msbP1 := i - 1
	if bin != 0 && i == numBins {
		msbP1 = numBins
	}
	if msbP1 > 1 {
		if msbP1 < numBins {
			bits := msbP1 - 1
			return c.bypassBits(bits) | 1<<uint(bits)
		}
		cur := 1 << uint(numBins-1)
		return c.truncBin(maxRun+1-cur) + cur
	}
	return msbP1
}

// paletteCU parses and reconstructs a palette coded CU.
func (d *Decoder) paletteCU(x0, y0, log2Cb int) error {
	s, sh := d.sp, d.sh
	c := &d.cab
	cu := &d.cu
	n := 1 << uint(log2Cb)
	total := n * n
	nc := 3
	if s.chromaFormat == 0 {
		nc = 1
	}
	pred := &d.palPred

	// palette entries: reused from the predictor, then new ones
	var pal [3][64]uint16
	var reused [128]bool
	numPred := 0
	if pred.size > 0 {
		sym := c.egk(0)
		if sym != 1 {
			idx := 0
			for idx < pred.size && numPred < s.paletteMaxSize {
				if idx > 0 {
					sym = c.egk(0)
				}
				if sym == 1 {
					break
				}
				if sym > 0 {
					idx += sym - 1
				}
				if idx >= pred.size {
					return errBitstream
				}
				reused[idx] = true
				numPred++
				idx++
			}
		}
	}
	k := 0
	for i := 0; i < pred.size; i++ {
		if reused[i] {
			for ci := 0; ci < nc; ci++ {
				pal[ci][k] = pred.ent[ci][i]
			}
			k++
		}
	}
	numNew := 0
	if numPred < s.paletteMaxSize {
		numNew = c.egk(0)
	}
	dictSize := numPred + numNew
	if dictSize > s.paletteMaxSize || dictSize > 64 {
		return errBitstream
	}
	for ci := 0; ci < nc; ci++ {
		bd := s.bitDepthY
		if ci > 0 {
			bd = s.bitDepthC
		}
		for i := numPred; i < dictSize; i++ {
			pal[ci][i] = uint16(c.bypassBits(bd))
		}
	}
	signalEscape := true
	if dictSize > 0 {
		signalEscape = c.bypass() != 0
	}
	indexMax := dictSize
	if signalEscape {
		indexMax++
	}

	scan := travScan(n)
	var (
		remIdx      = -1
		lastRunType = 0
		idxList     []int
		rotation    = false
	)
	if indexMax > 1 {
		num, ok := c.coeffRemain(3 + indexMax>>3)
		if !ok || num+1 > total {
			return errBitstream
		}
		num++
		remIdx = num
		adjust := 0
		for i := 0; i < num; i++ {
			idxList = append(idxList, c.truncBin(indexMax-adjust))
			adjust = 1
		}
		lastRunType = c.bin(ctxCopyAbove)
		rotation = c.bin(ctxPalTranspose) != 0
	}
	if signalEscape {
		if d.pp.cuQpDelta && !d.isCuQpDeltaCoded {
			if err := d.parseCuQpDelta(); err != nil {
				return err
			}
		}
		if sh.cuChromaQpOffsetEnabled && !cu.bypass && !d.isCuChromaQpOffsetCoded {
			d.parseCuChromaQpOffset()
		}
	}

	// index map
	var level [32 * 32]int16
	var spoint, esc [32 * 32]uint8
	idx := 0
	ip := 0
	for idx < total {
		tra := scan[idx]
		sp := 0
		if indexMax > 1 {
			if tra >= n && spoint[scan[idx-1]] != palRunAbove {
				if remIdx != 0 && idx < total-1 {
					sp = c.bin(ctxCopyAbove)
				} else if idx == total-1 && remIdx != 0 {
					sp = 0
				} else {
					sp = 1
				}
			}
		}
		spoint[tra] = uint8(sp)
		curLevel := 0
		pre := 0
		isEsc := false
		if sp == 0 {
			if ip < len(idxList) {
				curLevel = idxList[ip]
				ip++
			}
			ref := 1 << 30
			if idx > 0 {
				left := scan[idx-1]
				if spoint[left] == palRunLeft {
					ref = int(level[left])
					if esc[left] != 0 {
						ref = indexMax - 1
					}
				} else {
					if left < n {
						return errBitstream
					}
					ref = int(level[tra-n])
					if esc[tra-n] != 0 {
						ref = indexMax - 1
					}
				}
			}
			pre = curLevel
			if curLevel >= ref {
				pre++
			}
			level[tra] = int16(pre)
			isEsc = pre == dictSize
			if pre > dictSize {
				return errBitstream
			}
		}
		if isEsc {
			esc[tra] = 1
		} else {
			esc[tra] = 0
		}
		var run int
		if indexMax > 1 {
			if spoint[tra] == palRunLeft {
				remIdx--
			}
			lastRun := remIdx == 0 && int(spoint[tra]) == lastRunType
			if !lastRun {
				run = c.palRun(spoint[tra] == palRunAbove, curLevel, total-remIdx-idx-1-lastRunType)
			} else {
				run = total - idx - 1
			}
		} else {
			run = total - idx - 1
		}
		if run < 0 || idx+run >= total {
			return errBitstream
		}
		if spoint[tra] == palRunLeft {
			v := int16(pre)
			e := esc[tra]
			for p := 0; p < run; p++ {
				idx++
				tr := scan[idx]
				level[tr] = v
				spoint[tr] = palRunLeft
				esc[tr] = e
			}
			idx++
		} else {
			if tra < n {
				return errBitstream
			}
			level[tra] = level[tra-n]
			esc[tra] = esc[tra-n]
			for p := 0; p < run; p++ {
				idx++
				tr := scan[idx]
				level[tr] = level[tr-n]
				spoint[tr] = palRunAbove
				esc[tr] = esc[tr-n]
			}
			idx++
		}
	}

	// escape samples
	var escVal [3][32 * 32]uint16
	for ci := 0; ci < nc; ci++ {
		bd := s.bitDepthY
		sx, sy := uint(0), uint(0)
		if ci > 0 {
			bd = s.bitDepthC
			sx, sy = s.subW, s.subH
		}
		for i := 0; i < total; i++ {
			tra := scan[i]
			if esc[tra] == 0 {
				continue
			}
			if ci > 0 {
				x, y := tra%n, tra/n
				var ok bool
				switch {
				case s.chromaFormat == 3:
					ok = true
				case s.chromaFormat == 1:
					ok = x&1 == 0 && y&1 == 0
				case !rotation:
					ok = x&1 == 0
				default:
					ok = y&1 == 0
				}
				if !ok {
					continue
				}
			}
			_ = sx
			_ = sy
			var v int
			if cu.bypass {
				v = c.bypassBits(bd)
			} else {
				v = c.egk(3)
				if v >= 1<<uint(bd+1) {
					return errBitstream
				}
			}
			escVal[ci][tra] = uint16(v)
		}
	}

	// reconstruction
	pic := d.cur
	for ci := 0; ci < nc; ci++ {
		bd := s.bitDepthY
		var sx, sy uint
		if ci > 0 {
			bd = s.bitDepthC
			sx, sy = s.subW, s.subH
		}
		maxV := int32(1)<<uint(bd) - 1
		var qp int
		if !cu.bypass {
			if ci == 0 {
				qp = d.qpY + s.qpBdOffY
			} else {
				qp = d.chromaQp(ci)
			}
		}
		scale := levelScale[qp%6] << uint(qp/6)
		cw, ch := n>>sx, n>>sy
		xc, yc := x0>>sx, y0>>sy
		stride := pic.stride[ci]
		pl := pic.planes[ci]
		for y := 0; y < ch; y++ {
			row := pl[(yc+y)*stride+xc : (yc+y)*stride+xc+cw]
			for x := range row {
				lx, ly := x<<sx, y<<sy
				pi := ly*n + lx // index in the (non-rotated) parse domain
				if rotation {
					pi = lx*n + ly
				}
				if esc[pi] != 0 {
					v := int64(escVal[ci][pi])
					if !cu.bypass {
						v = (v*scale + 32) >> 6
						if v > int64(maxV) {
							v = int64(maxV)
						}
					}
					row[x] = uint16(v)
				} else {
					row[x] = pal[ci][level[pi]]
				}
			}
		}
	}

	// update the predictor: the palette followed by the unused entries
	var np palPredictor
	for ci := 0; ci < nc; ci++ {
		cnt := min(dictSize, s.palettePredMax)
		copy(np.ent[ci][:cnt], pal[ci][:cnt])
		m := cnt
		for i := 0; i < pred.size && m < s.palettePredMax; i++ {
			if !reused[i] {
				np.ent[ci][m] = pred.ent[ci][i]
				m++
			}
		}
		np.size = m
	}
	*pred = np
	return nil
}

var travScans [7][]int

func init() {
	for l := 2; l <= 6; l++ {
		n := 1 << uint(l)
		t := make([]int, 0, n*n)
		for y := 0; y < n; y++ {
			if y&1 == 0 {
				for x := 0; x < n; x++ {
					t = append(t, y*n+x)
				}
			} else {
				for x := n - 1; x >= 0; x-- {
					t = append(t, y*n+x)
				}
			}
		}
		travScans[l] = t
	}
}

// travScan returns the traverse (snake) scan of an n x n block as raster
// indices.
func travScan(n int) []int { return travScans[bitLen(n)-1] }
