package av1

import "math/bits"

// symDec is the multi-symbol adaptive range decoder of spec section 8.2,
// implemented with a 64-bit window in the style of the reference decoders.
// dif holds the (inverted) bitstream; its top 16 bits are SymbolValue.
type symDec struct {
	buf      []byte
	pos      int
	dif      uint64
	rng      uint32
	cnt      int
	noUpdate bool
}

func (s *symDec) init(data []byte, disableCdfUpdate bool) {
	s.buf = data
	s.pos = 0
	s.rng = 0x8000
	s.cnt = -15
	s.dif = (uint64(1) << 63) - 1
	s.noUpdate = disableCdfUpdate
	s.refill()
}

func (s *symDec) refill() {
	c := 64 - s.cnt - 24
	dif := s.dif
	for c >= 0 && s.pos < len(s.buf) {
		dif ^= uint64(s.buf[s.pos]) << uint(c)
		s.pos++
		c -= 8
	}
	s.dif = dif
	s.cnt = 64 - c - 24
}

func (s *symDec) norm(dif uint64, rng uint32) {
	d := bits.LeadingZeros32(rng) - 16
	s.cnt -= d
	s.dif = ((dif + 1) << uint(d)) - 1
	s.rng = rng << uint(d)
	if s.cnt < 0 {
		s.refill()
	}
}

// symbol decodes one symbol using (and adapting) cdf, which holds N
// cumulative probabilities followed by the adaptation counter.
func (s *symDec) symbol(cdf []uint16) int {
	n := len(cdf) - 1
	c := uint32(s.dif >> 48)
	r := s.rng >> 8
	u, v := uint32(0), s.rng
	val := -1
	for {
		val++
		u = v
		f := 32768 - uint32(cdf[val])
		v = ((r * (f >> 6)) >> 1) + 4*uint32(n-val-1)
		if c >= v {
			break
		}
	}
	s.norm(s.dif-(uint64(v)<<48), u-v)
	if !s.noUpdate {
		cnt := cdf[n]
		rate := uint(3 + b2i(cnt > 15) + b2i(cnt > 31))
		if n >= 4 {
			rate += 2
		} else {
			rate++
		}
		for i := 0; i < n-1; i++ {
			if i < val {
				cdf[i] -= cdf[i] >> rate
			} else {
				cdf[i] += (32768 - cdf[i]) >> rate
			}
		}
		if cnt < 32 {
			cdf[n] = cnt + 1
		}
	}
	return val
}

// symbolNoAdapt decodes with a temporary cdf that is not adapted.
func (s *symDec) symbolTmp(cdf []uint16) int {
	n := len(cdf) - 1
	c := uint32(s.dif >> 48)
	r := s.rng >> 8
	u, v := uint32(0), s.rng
	val := -1
	for {
		val++
		u = v
		f := 32768 - uint32(cdf[val])
		v = ((r * (f >> 6)) >> 1) + 4*uint32(n-val-1)
		if c >= v {
			break
		}
	}
	s.norm(s.dif-(uint64(v)<<48), u-v)
	return val
}

// boolean decodes an equiprobable bit (read_bool).
func (s *symDec) boolean() int {
	r := s.rng
	dif := s.dif
	v := ((r >> 8) << 7) + 4
	vw := uint64(v) << 48
	ret := uint32(0)
	if dif >= vw {
		ret = 1
		dif -= vw
		v = r - v
	}
	s.norm(dif, v)
	return int(1 - ret)
}

// literal reads an n-bit unsigned value (L(n)).
func (s *symDec) literal(n int) int {
	x := 0
	for i := 0; i < n; i++ {
		x = x<<1 | s.boolean()
	}
	return x
}

// ns reads a non-symmetric value in 0..n-1 using read_literal (NS(n)).
func (s *symDec) ns(n int) int {
	w := 0
	for x := n; x != 0; x >>= 1 {
		w++
	}
	m := (1 << uint(w)) - n
	v := s.literal(w - 1)
	if v < m {
		return v
	}
	return (v << 1) - m + s.literal(1)
}
