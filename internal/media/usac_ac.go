package media

// Context-based arithmetic spectral noiseless coder of USAC (ISO/IEC 23003-3
// 7.4). The context handling and the decoder follow FFmpeg's
// libavcodec/aac/aacdec_ac.c (LGPL-2.1-or-later, see NOTICE).

const usacAcEscape = 16

type usacArithState struct {
	last    [513]uint8
	lastLen int
	cur     [4]uint8
	pre     uint16
}

type usacArith struct {
	low, high, val int
}

// mapProcess prepares the context for a new spectrum of N lines.
func (s *usacArithState) mapProcess(reset bool, n int) uint32 {
	if reset {
		s.last = [513]uint8{}
		s.lastLen = n
	} else if s.lastLen != n {
		var last [512]uint8
		copy(last[:], s.last[:512])
		ratio := float32(s.lastLen) / float32(n)
		i := 0
		for ; i < n/2; i++ {
			k := int(float32(i) * ratio)
			s.last[i] = last[k]
		}
		for ; i < len(s.last); i++ {
			s.last[i] = 0
		}
		s.lastLen = n
	}
	s.cur[3], s.cur[2], s.cur[1], s.cur[0] = 0, 0, 0, 1
	s.pre = uint16(s.last[0]) << 12
	return uint32(s.last[0]) << 12
}

func (s *usacArithState) getContext(i int) uint32 {
	c := uint32(s.pre) >> 8
	c += uint32(s.last[i+1]) << 8
	c <<= 4
	c += uint32(s.cur[1])
	s.pre = uint16(c)
	if i > 3 && int(s.cur[3])+int(s.cur[2])+int(s.cur[1]) < 5 {
		return c + 0x10000
	}
	return c
}

func usacAcGetPk(c uint32) uint32 {
	iMin := -1
	iMax := len(usacAcLookupM) - 1
	for iMax-iMin > 1 {
		i := iMin + (iMax-iMin)/2
		j := usacAcHashM[i]
		switch {
		case c < j>>8:
			iMax = i
		case c > j>>8:
			iMin = i
		default:
			return j & 0xff
		}
	}
	return uint32(usacAcLookupM[iMax])
}

func (s *usacArithState) update(idx int, a, b int) {
	v := a + b + 1
	if v > 0xf {
		v = 0xf
	}
	s.cur[0] = uint8(v)
	s.cur[3] = s.cur[2]
	s.cur[2] = s.cur[1]
	s.cur[1] = s.cur[0]
	s.last[idx] = s.cur[0]
}

func (s *usacArithState) finish(offset, n int) {
	i := offset
	for ; i < n/2; i++ {
		s.last[i] = 1
	}
	for ; i < len(s.last); i++ {
		s.last[i] = 0
	}
}

func (a *usacArith) init(r *aacBits) {
	a.low = 0
	a.high = 65535
	a.val = r.bits(16)
}

// decode returns the symbol of the cumulative frequency table cdf (decreasing,
// terminated by 0).
func (a *usacArith) decode(r *aacBits, cdf []uint16) int {
	val, low, high := a.val, a.low, a.high
	rng := high - low + 1
	c := ((val - low + 1) << 14) - 1
	// first symbol whose scaled cumulative frequency does not exceed c
	n := len(cdf)
	lo, hi := 0, n-1
	for lo < hi {
		mid := (lo + hi) >> 1
		if int(cdf[mid])*rng > c {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	sym := lo
	if sym > 0 {
		high = low + ((rng * int(cdf[sym-1])) >> 14) - 1
	}
	low += (rng * int(cdf[sym])) >> 14
	for {
		if high < 32768 {
		} else if low >= 32768 {
			val -= 32768
			low -= 32768
			high -= 32768
		} else if low >= 16384 && high < 49152 {
			val -= 16384
			low -= 16384
			high -= 16384
		} else {
			break
		}
		low += low
		high += high + 1
		val = val<<1 | r.bits(1)
	}
	a.low, a.high, a.val = low, high, val
	return sym
}

// decodeSpectrumAC reads len/2 pairs of quantised spectral lines (signs
// included) of a spectrum with N lines into q. It returns false on an
// invalid bitstream.
func usacDecodeSpectrumAC(q []int32, r *aacBits, st *usacArithState, reset bool, ln, n int) bool {
	c := st.mapProcess(reset, n)
	_ = c
	if ln == 0 {
		st.finish(0, n)
		return true
	}
	var ac usacArith
	// the decoder reads ahead of the point where the data ends (possibly
	// past the end of the access unit): it works on a copy of the reader and
	// the original is positioned afterwards
	g := &aacBits{b: r.b, pos: r.pos}
	ac.init(g)
	i := 0
	for ; i < ln/2; i++ {
		cc := st.getContext(i)
		lvl, escNb := 0, 0
		var m int
		for {
			pki := usacAcGetPk(cc + uint32(escNb)<<17)
			m = ac.decode(g, usacAcMsbCdfs[pki*17:pki*17+17])
			if m < usacAcEscape {
				break
			}
			lvl++
			if lvl > 23 {
				return false
			}
			escNb = lvl
			if escNb > 7 {
				escNb = 7
			}
		}
		b := m >> 2
		a := m - b<<2
		if m == 0 {
			if escNb != 0 {
				break // ARITH_STOP
			}
			a, b = 0, 0
		}
		for l := lvl; l > 0; l-- {
			lsb := 2
			if a == 0 {
				lsb = 1
			} else if b == 0 {
				lsb = 0
			}
			rr := ac.decode(g, usacAcLsbCdfs[lsb*4:lsb*4+4])
			a = a<<1 | rr&1
			b = b<<1 | rr>>1&1
		}
		q[2*i], q[2*i+1] = int32(a), int32(b)
		st.update(i, a, b)
	}
	if ln > 1 {
		r.pos = g.pos - 14
	} else {
		r.pos = g.pos
	}
	if r.pos > len(r.b)*8 {
		r.err = true
	}
	st.finish(i, n)
	for ; i < n/2; i++ {
		q[2*i], q[2*i+1] = 0, 0
	}
	// signs
	for i := 0; i < ln; i++ {
		if q[i] != 0 && r.bits(1) == 0 {
			q[i] = -q[i]
		}
	}
	return !r.err
}
