package av1

// ---- film grain synthesis (spec 7.18.3) -------------------------------------------

type grainRand struct{ r int }

func (g *grainRand) next(bits uint) int {
	r := g.r
	bit := ((r >> 0) ^ (r >> 1) ^ (r >> 3) ^ (r >> 12)) & 1
	r = (r >> 1) | (bit << 15)
	g.r = r
	return (r >> (16 - bits)) & ((1 << bits) - 1)
}

type grainState struct {
	bd                 int
	grainMin, grainMax int
	luma               [73][82]int
	cb, cr             [38 + 35][82]int // sized for the largest chroma array (73 x 82)
	scaling            [3][256]int
}

func applyFilmGrain(pic *Picture, fg *filmGrain) *Picture {
	cov(covFilmGrain)
	out := &Picture{}
	*out = *pic
	numPlanes := 3
	if pic.Mono {
		numPlanes = 1
	}
	w, h := pic.Width, pic.Height
	subX, subY := pic.SubX, pic.SubY
	bd := pic.BitDepth
	// copy visible samples
	for p := 0; p < numPlanes; p++ {
		pw, ph := w, h
		if p > 0 {
			pw = (w + subX) >> uint(subX)
			ph = (h + subY) >> uint(subY)
		}
		buf := make([]uint16, pw*ph)
		for y := 0; y < ph; y++ {
			copy(buf[y*pw:(y+1)*pw], pic.Planes[p][y*pic.Stride[p]:y*pic.Stride[p]+pw])
		}
		out.Planes[p] = buf
		out.Stride[p] = pw
	}
	gs := &grainState{bd: bd}
	grainCenter := 128 << uint(bd-8)
	gs.grainMin = -grainCenter
	gs.grainMax = (256 << uint(bd-8)) - 1 - grainCenter
	rng := &grainRand{r: fg.seed}
	gs.generateGrain(fg, rng, pic.Mono, subX, subY)
	gs.initScaling(fg, numPlanes)
	gs.addNoise(fg, out, w, h, subX, subY, numPlanes)
	return out
}

func (gs *grainState) generateGrain(fg *filmGrain, rng *grainRand, mono bool, subX, subY int) {
	bd := gs.bd
	shift := uint(12 - bd + fg.grainScaleShift)
	for y := 0; y < 73; y++ {
		for x := 0; x < 82; x++ {
			g := 0
			if fg.numYPoints > 0 {
				g = int(gaussianSequence[rng.next(11)])
			}
			gs.luma[y][x] = round2(g, shift)
		}
	}
	arShift := uint(fg.arCoeffShift)
	lag := fg.arCoeffLag
	for y := 3; y < 73; y++ {
		for x := 3; x < 82-3; x++ {
			s := 0
			pos := 0
			for dr := -lag; dr <= 0; dr++ {
				for dc := -lag; dc <= lag; dc++ {
					if dr == 0 && dc == 0 {
						break
					}
					c := fg.arCoeffsY[pos]
					s += gs.luma[y+dr][x+dc] * c
					pos++
				}
			}
			gs.luma[y][x] = clip3(gs.grainMin, gs.grainMax, gs.luma[y][x]+round2(s, arShift))
		}
	}
	if mono {
		return
	}
	chromaW := 82
	if subX != 0 {
		chromaW = 44
	}
	chromaH := 73
	if subY != 0 {
		chromaH = 38
	}
	rng.r = fg.seed ^ 0xb524
	for y := 0; y < chromaH; y++ {
		for x := 0; x < chromaW; x++ {
			g := 0
			if fg.numCbPoints > 0 || fg.chromaFromLuma {
				g = int(gaussianSequence[rng.next(11)])
			}
			gs.cb[y][x] = round2(g, shift)
		}
	}
	rng.r = fg.seed ^ 0x49d8
	for y := 0; y < chromaH; y++ {
		for x := 0; x < chromaW; x++ {
			g := 0
			if fg.numCrPoints > 0 || fg.chromaFromLuma {
				g = int(gaussianSequence[rng.next(11)])
			}
			gs.cr[y][x] = round2(g, shift)
		}
	}
	for y := 3; y < chromaH; y++ {
		for x := 3; x < chromaW-3; x++ {
			s0, s1 := 0, 0
			pos := 0
			for dr := -lag; dr <= 0; dr++ {
				for dc := -lag; dc <= lag; dc++ {
					c0 := fg.arCoeffsCb[pos]
					c1 := fg.arCoeffsCr[pos]
					if dr == 0 && dc == 0 {
						if fg.numYPoints > 0 {
							luma := 0
							lumaX := ((x - 3) << uint(subX)) + 3
							lumaY := ((y - 3) << uint(subY)) + 3
							for i := 0; i <= subY; i++ {
								for j := 0; j <= subX; j++ {
									luma += gs.luma[lumaY+i][lumaX+j]
								}
							}
							luma = round2(luma, uint(subX+subY))
							s0 += luma * c0
							s1 += luma * c1
						}
						break
					}
					s0 += gs.cb[y+dr][x+dc] * c0
					s1 += gs.cr[y+dr][x+dc] * c1
					pos++
				}
			}
			gs.cb[y][x] = clip3(gs.grainMin, gs.grainMax, gs.cb[y][x]+round2(s0, arShift))
			gs.cr[y][x] = clip3(gs.grainMin, gs.grainMax, gs.cr[y][x]+round2(s1, arShift))
		}
	}
}

func (gs *grainState) initScaling(fg *filmGrain, numPlanes int) {
	for plane := 0; plane < numPlanes; plane++ {
		var xs, ys []int
		n := 0
		if plane == 0 || fg.chromaFromLuma {
			n = fg.numYPoints
			xs, ys = fg.yValue[:], fg.yScaling[:]
		} else if plane == 1 {
			n = fg.numCbPoints
			xs, ys = fg.cbValue[:], fg.cbScale[:]
		} else {
			n = fg.numCrPoints
			xs, ys = fg.crValue[:], fg.crScale[:]
		}
		lut := &gs.scaling[plane]
		if n == 0 {
			for x := 0; x < 256; x++ {
				lut[x] = 0
			}
			continue
		}
		for x := 0; x < xs[0]; x++ {
			lut[x] = ys[0]
		}
		for i := 0; i < n-1; i++ {
			deltaY := ys[i+1] - ys[i]
			deltaX := xs[i+1] - xs[i]
			delta := deltaY * ((65536 + (deltaX >> 1)) / deltaX)
			for x := 0; x < deltaX; x++ {
				v := ys[i] + ((x*delta + 32768) >> 16)
				lut[xs[i]+x] = v
			}
		}
		for x := xs[n-1]; x < 256; x++ {
			lut[x] = ys[n-1]
		}
	}
}

func (gs *grainState) scaleLut(plane, index int) int {
	shift := uint(gs.bd - 8)
	x := index >> shift
	rem := index - (x << shift)
	if gs.bd == 8 || x == 255 {
		return gs.scaling[plane][x]
	}
	start := gs.scaling[plane][x]
	end := gs.scaling[plane][x+1]
	return start + round2((end-start)*rem, shift)
}

func (gs *grainState) addNoise(fg *filmGrain, out *Picture, w, h, subX, subY, numPlanes int) {
	bd := gs.bd
	// noise stripes
	nStripes := 0
	for y := 0; y < (h+1)/2; y += 16 {
		nStripes++
	}
	stripeW := w + 80
	type stripe [3][]int
	stripes := make([]stripe, nStripes)
	for i := range stripes {
		for p := 0; p < numPlanes; p++ {
			rows := 34
			if p > 0 && subY != 0 {
				rows = 17
			}
			stripes[i][p] = make([]int, rows*stripeW)
		}
	}
	rng := &grainRand{}
	lumaNum := 0
	for y := 0; y < (h+1)/2; y += 16 {
		rng.r = fg.seed
		rng.r ^= ((lumaNum*37 + 178) & 255) << 8
		rng.r ^= (lumaNum*173 + 105) & 255
		for x := 0; x < (w+1)/2; x += 16 {
			rand := rng.next(8)
			offsetX := rand >> 4
			offsetY := rand & 15
			for plane := 0; plane < numPlanes; plane++ {
				planeSubX, planeSubY := 0, 0
				if plane > 0 {
					planeSubX, planeSubY = subX, subY
				}
				planeOffsetX := 9 + offsetX*2
				if planeSubX != 0 {
					planeOffsetX = 6 + offsetX
				}
				planeOffsetY := 9 + offsetY*2
				if planeSubY != 0 {
					planeOffsetY = 6 + offsetY
				}
				ns := stripes[lumaNum][plane]
				for i := 0; i < 34>>uint(planeSubY); i++ {
					for j := 0; j < 34>>uint(planeSubX); j++ {
						var g int
						switch plane {
						case 0:
							g = gs.luma[planeOffsetY+i][planeOffsetX+j]
						case 1:
							g = gs.cb[planeOffsetY+i][planeOffsetX+j]
						default:
							g = gs.cr[planeOffsetY+i][planeOffsetX+j]
						}
						if planeSubX == 0 {
							idx := i*stripeW + x*2 + j
							if j < 2 && fg.overlap && x > 0 {
								old := ns[idx]
								if j == 0 {
									g = old*27 + g*17
								} else {
									g = old*17 + g*27
								}
								g = clip3(gs.grainMin, gs.grainMax, round2(g, 5))
							}
							ns[idx] = g
						} else {
							idx := i*stripeW + x + j
							if j == 0 && fg.overlap && x > 0 {
								old := ns[idx]
								g = old*23 + g*22
								g = clip3(gs.grainMin, gs.grainMax, round2(g, 5))
							}
							ns[idx] = g
						}
					}
				}
			}
		}
		lumaNum++
	}
	// noise image
	var noiseImage [3][]int
	for plane := 0; plane < numPlanes; plane++ {
		planeSubX, planeSubY := 0, 0
		if plane > 0 {
			planeSubX, planeSubY = subX, subY
		}
		pw := (w + planeSubX) >> uint(planeSubX)
		ph := (h + planeSubY) >> uint(planeSubY)
		img := make([]int, pw*ph)
		for y := 0; y < ph; y++ {
			ln := y >> uint(5-planeSubY)
			i := y - (ln << uint(5-planeSubY))
			for x := 0; x < pw; x++ {
				g := stripes[ln][plane][i*stripeW+x]
				if planeSubY == 0 {
					if i < 2 && ln > 0 && fg.overlap {
						old := stripes[ln-1][plane][(i+32)*stripeW+x]
						if i == 0 {
							g = old*27 + g*17
						} else {
							g = old*17 + g*27
						}
						g = clip3(gs.grainMin, gs.grainMax, round2(g, 5))
					}
				} else {
					if i < 1 && ln > 0 && fg.overlap {
						old := stripes[ln-1][plane][(i+16)*stripeW+x]
						g = old*23 + g*22
						g = clip3(gs.grainMin, gs.grainMax, round2(g, 5))
					}
				}
				img[y*pw+x] = g
			}
		}
		noiseImage[plane] = img
	}
	// blend
	var minValue, maxLuma, maxChroma int
	if fg.clipRestricted {
		minValue = 16 << uint(bd-8)
		maxLuma = 235 << uint(bd-8)
		if out.MatrixCoefficients == 0 {
			maxChroma = maxLuma
		} else {
			maxChroma = 240 << uint(bd-8)
		}
	} else {
		minValue = 0
		maxLuma = (256 << uint(bd-8)) - 1
		maxChroma = maxLuma
	}
	scalingShift := uint(fg.scalingShift)
	maxPix := (1 << uint(bd)) - 1
	outY := out.Planes[0]
	ys := out.Stride[0]
	if numPlanes > 1 {
		cw := (w + subX) >> uint(subX)
		ch := (h + subY) >> uint(subY)
		outU, outV := out.Planes[1], out.Planes[2]
		cs := out.Stride[1]
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				lumaX := x << uint(subX)
				lumaY := y << uint(subY)
				lumaNextX := imin(lumaX+1, w-1)
				var averageLuma int
				if subX != 0 {
					averageLuma = round2(int(outY[lumaY*ys+lumaX])+int(outY[lumaY*ys+lumaNextX]), 1)
				} else {
					averageLuma = int(outY[lumaY*ys+lumaX])
				}
				if fg.numCbPoints > 0 || fg.chromaFromLuma {
					orig := int(outU[y*cs+x])
					var merged int
					if fg.chromaFromLuma {
						merged = averageLuma
					} else {
						combined := averageLuma*(fg.cbLumaMul-128) + orig*(fg.cbMult-128)
						merged = clip3(0, maxPix, (combined>>6)+((fg.cbOffset-256)<<uint(bd-8)))
					}
					noise := noiseImage[1][y*cw+x]
					noise = round2(gs.scaleLut(1, merged)*noise, scalingShift)
					outU[y*cs+x] = uint16(clip3(minValue, maxChroma, orig+noise))
				}
				if fg.numCrPoints > 0 || fg.chromaFromLuma {
					orig := int(outV[y*cs+x])
					var merged int
					if fg.chromaFromLuma {
						merged = averageLuma
					} else {
						combined := averageLuma*(fg.crLumaMul-128) + orig*(fg.crMult-128)
						merged = clip3(0, maxPix, (combined>>6)+((fg.crOffset-256)<<uint(bd-8)))
					}
					noise := noiseImage[2][y*cw+x]
					noise = round2(gs.scaleLut(2, merged)*noise, scalingShift)
					outV[y*cs+x] = uint16(clip3(minValue, maxChroma, orig+noise))
				}
			}
		}
	}
	if fg.numYPoints > 0 {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				orig := int(outY[y*ys+x])
				noise := noiseImage[0][y*w+x]
				noise = round2(gs.scaleLut(0, orig)*noise, scalingShift)
				outY[y*ys+x] = uint16(clip3(minValue, maxLuma, orig+noise))
			}
		}
	}
}
