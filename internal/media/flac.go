package media

import (
	"errors"
	"fmt"
)

// Native FLAC decoder (all subframe types, stereo decorrelation, 4..32 bit).

type flacBits struct {
	b   []byte
	pos int // bit position
}

func (r *flacBits) bit() (int, error) {
	if r.pos>>3 >= len(r.b) {
		return 0, errTrunc
	}
	v := int(r.b[r.pos>>3]>>(7-uint(r.pos&7))) & 1
	r.pos++
	return v, nil
}

var errTrunc = errors.New("flac: truncated")

func (r *flacBits) bits(n int) (uint64, error) {
	if n == 0 {
		return 0, nil
	}
	if (r.pos+n+7)>>3 > len(r.b) {
		return 0, errTrunc
	}
	var v uint64
	for n > 0 {
		avail := 8 - r.pos&7
		take := avail
		if take > n {
			take = n
		}
		cur := uint64(r.b[r.pos>>3]>>(uint(avail-take))) & (1<<uint(take) - 1)
		v = v<<uint(take) | cur
		r.pos += take
		n -= take
	}
	return v, nil
}

func (r *flacBits) signed(n int) (int64, error) {
	v, err := r.bits(n)
	if err != nil {
		return 0, err
	}
	if n > 0 && v&(1<<uint(n-1)) != 0 {
		return int64(v) - 1<<uint(n), nil
	}
	return int64(v), nil
}

func (r *flacBits) unary() (int, error) {
	n := 0
	for {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		if b == 1 {
			return n, nil
		}
		n++
	}
}

func (r *flacBits) align() { r.pos = (r.pos + 7) &^ 7 }

func (r *flacBits) utf8() (uint64, error) {
	b0, err := r.bits(8)
	if err != nil {
		return 0, err
	}
	n := 0
	switch {
	case b0&0x80 == 0:
		return b0, nil
	case b0&0xE0 == 0xC0:
		n = 1
	case b0&0xF0 == 0xE0:
		n = 2
	case b0&0xF8 == 0xF0:
		n = 3
	case b0&0xFC == 0xF8:
		n = 4
	case b0&0xFE == 0xFC:
		n = 5
	case b0 == 0xFE:
		n = 6
	default:
		return 0, fmt.Errorf("flac: bad utf8 number")
	}
	v := b0 & (0x3F >> uint(n))
	if n == 6 {
		v = 0
	}
	for i := 0; i < n; i++ {
		c, err := r.bits(8)
		if err != nil {
			return 0, err
		}
		v = v<<6 | c&0x3F
	}
	return v, nil
}

// DecodeFLAC decodes a whole FLAC stream.
func DecodeFLAC(data []byte) (*PCM, error) {
	si, err := parseFLAC(bytesReaderAt(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	out := &PCM{SampleRate: si.SampleRate, Data: make([][]float32, si.Channels)}
	if si.TotalSamples > 0 && si.TotalSamples < 1<<31 {
		for c := range out.Data {
			out.Data[c] = make([]float32, 0, si.TotalSamples)
		}
	}
	br := &flacBits{b: data, pos: int(si.FirstFrame) * 8}
	for br.pos>>3+4 < len(data) {
		sync, err := br.bits(14)
		if err != nil || sync != 0x3FFE {
			break
		}
		br.bits(2) // reserved + blocking strategy
		bsc, _ := br.bits(4)
		src, _ := br.bits(4)
		cha, _ := br.bits(4)
		ssc, _ := br.bits(3)
		br.bits(1)
		if _, err := br.utf8(); err != nil {
			return nil, err
		}
		var bs int
		switch {
		case bsc == 1:
			bs = 192
		case bsc >= 2 && bsc <= 5:
			bs = 576 << (bsc - 2)
		case bsc == 6:
			v, _ := br.bits(8)
			bs = int(v) + 1
		case bsc == 7:
			v, _ := br.bits(16)
			bs = int(v) + 1
		case bsc >= 8:
			bs = 256 << (bsc - 8)
		default:
			return nil, fmt.Errorf("flac: reserved block size")
		}
		switch {
		case src == 12:
			br.bits(8)
		case src == 13 || src == 14:
			br.bits(16)
		}
		br.bits(8) // header crc
		bps := si.Bits
		switch ssc {
		case 1:
			bps = 8
		case 2:
			bps = 12
		case 4:
			bps = 16
		case 5:
			bps = 20
		case 6:
			bps = 24
		case 7:
			bps = 32
		}
		nch := int(cha) + 1
		if cha >= 8 {
			nch = 2
		}
		if nch != si.Channels {
			// tolerate but the output shape is fixed by STREAMINFO
			if nch > si.Channels {
				return nil, fmt.Errorf("flac: channel count changed")
			}
		}
		chans := make([][]int64, nch)
		for c := 0; c < nch; c++ {
			b := bps
			if (cha == 8 && c == 1) || (cha == 9 && c == 0) || (cha == 10 && c == 1) {
				b++
			}
			s, err := flacSubframe(br, bs, b)
			if err != nil {
				return nil, err
			}
			chans[c] = s
		}
		br.align()
		br.bits(16)
		switch cha {
		case 8:
			for i := 0; i < bs; i++ {
				chans[1][i] = chans[0][i] - chans[1][i]
			}
		case 9:
			for i := 0; i < bs; i++ {
				chans[0][i] = chans[0][i] + chans[1][i]
			}
		case 10:
			for i := 0; i < bs; i++ {
				mid, side := chans[0][i], chans[1][i]
				mid = mid<<1 | side&1
				chans[0][i] = (mid + side) >> 1
				chans[1][i] = (mid - side) >> 1
			}
		}
		scale := float32(1) / float32(int64(1)<<uint(bps-1))
		for c := 0; c < nch && c < len(out.Data); c++ {
			for i := 0; i < bs; i++ {
				out.Data[c] = append(out.Data[c], float32(chans[c][i])*scale)
			}
		}
	}
	if len(out.Data) == 0 || len(out.Data[0]) == 0 {
		return nil, fmt.Errorf("flac: no audio frames")
	}
	return out, nil
}

func flacSubframe(br *flacBits, bs, bps int) ([]int64, error) {
	if _, err := br.bit(); err != nil {
		return nil, err
	}
	typ, err := br.bits(6)
	if err != nil {
		return nil, err
	}
	wasted := 0
	if w, _ := br.bit(); w == 1 {
		k, err := br.unary()
		if err != nil {
			return nil, err
		}
		wasted = k + 1
	}
	b := bps - wasted
	s := make([]int64, bs)
	switch {
	case typ == 0:
		v, err := br.signed(b)
		if err != nil {
			return nil, err
		}
		for i := range s {
			s[i] = v
		}
	case typ == 1:
		for i := range s {
			v, err := br.signed(b)
			if err != nil {
				return nil, err
			}
			s[i] = v
		}
	case typ >= 8 && typ <= 12:
		order := int(typ & 7)
		for i := 0; i < order && i < bs; i++ {
			v, err := br.signed(b)
			if err != nil {
				return nil, err
			}
			s[i] = v
		}
		if err := flacResidual(br, s, order, bs); err != nil {
			return nil, err
		}
		for i := order; i < bs; i++ {
			switch order {
			case 1:
				s[i] += s[i-1]
			case 2:
				s[i] += 2*s[i-1] - s[i-2]
			case 3:
				s[i] += 3*s[i-1] - 3*s[i-2] + s[i-3]
			case 4:
				s[i] += 4*s[i-1] - 6*s[i-2] + 4*s[i-3] - s[i-4]
			}
		}
	case typ >= 32:
		order := int(typ&31) + 1
		for i := 0; i < order && i < bs; i++ {
			v, err := br.signed(b)
			if err != nil {
				return nil, err
			}
			s[i] = v
		}
		pr, _ := br.bits(4)
		prec := int(pr) + 1
		shift, _ := br.signed(5)
		coef := make([]int64, order)
		for i := range coef {
			c, err := br.signed(prec)
			if err != nil {
				return nil, err
			}
			coef[i] = c
		}
		if err := flacResidual(br, s, order, bs); err != nil {
			return nil, err
		}
		for i := order; i < bs; i++ {
			var sum int64
			for j := 0; j < order; j++ {
				sum += coef[j] * s[i-1-j]
			}
			if shift >= 0 {
				s[i] += sum >> uint(shift)
			} else {
				s[i] += sum << uint(-shift)
			}
		}
	default:
		return nil, fmt.Errorf("flac: bad subframe type %d", typ)
	}
	if wasted > 0 {
		for i := range s {
			s[i] <<= uint(wasted)
		}
	}
	return s, nil
}

func flacResidual(br *flacBits, s []int64, order, bs int) error {
	method, err := br.bits(2)
	if err != nil {
		return err
	}
	if method > 1 {
		return fmt.Errorf("flac: bad residual method")
	}
	po, _ := br.bits(4)
	parts := 1 << po
	pbits := 4
	esc := uint64(15)
	if method == 1 {
		pbits, esc = 5, 31
	}
	idx := order
	for p := 0; p < parts; p++ {
		n := bs >> po
		if p == 0 {
			n -= order
		}
		param, err := br.bits(pbits)
		if err != nil {
			return err
		}
		if param == esc {
			nb, _ := br.bits(5)
			for i := 0; i < n; i++ {
				v, err := br.signed(int(nb))
				if err != nil {
					return err
				}
				if idx < bs {
					s[idx] = v
				}
				idx++
			}
			continue
		}
		for i := 0; i < n; i++ {
			q, err := br.unary()
			if err != nil {
				return err
			}
			r, err := br.bits(int(param))
			if err != nil {
				return err
			}
			u := uint64(q)<<param | r
			v := int64(u>>1) ^ -int64(u&1)
			if idx < bs {
				s[idx] = v
			}
			idx++
		}
	}
	return nil
}
