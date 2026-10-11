package vp9

// Inverse transforms (spec 8.7), implemented from the butterfly description.

var cos64Lookup = [33]int32{
	16384, 16364, 16305, 16207, 16069, 15893, 15679, 15426,
	15137, 14811, 14449, 14053, 13623, 13160, 12665, 12140,
	11585, 11003, 10394, 9760, 9102, 8423, 7723, 7005,
	6270, 5520, 4756, 3981, 3196, 2404, 1606, 804, 0,
}

func cos64(angle int) int64 {
	a := angle & 127
	switch {
	case a <= 32:
		return int64(cos64Lookup[a])
	case a <= 64:
		return -int64(cos64Lookup[64-a])
	case a <= 96:
		return -int64(cos64Lookup[a-64])
	}
	return int64(cos64Lookup[128-a])
}

func sin64(angle int) int64 { return cos64(angle - 32) }

func brev(n uint, x int) int {
	t := 0
	for i := uint(0); i < n; i++ {
		t |= ((x >> i) & 1) << (n - 1 - i)
	}
	return t
}

type dctOp struct {
	kind   uint8 // 0 = B, 1 = H
	a, b   uint8
	flip   bool
	cs, sn int64
}

var dctOps [6][]dctOp // by n (2..5)
var dctPerm [6][]uint8

func buildDCT(n uint, ops *[]dctOp) {
	n0 := 1 << n
	n1 := n0 >> 1
	n2 := n0 >> 2
	n3 := n0 >> 3
	B := func(a, b, angle int, flip bool) {
		*ops = append(*ops, dctOp{0, uint8(a), uint8(b), flip, cos64(angle), sin64(angle)})
	}
	H := func(a, b int, flip bool) {
		if flip {
			a, b = b, a
		}
		*ops = append(*ops, dctOp{1, uint8(a), uint8(b), false, 0, 0})
	}
	if n == 2 {
		B(0, 1, 16, true)
	} else {
		buildDCT(n-1, ops)
	}
	for i := 0; i < n2; i++ {
		B(n1+i, n0-1-i, 32-brev(5, n1+i), false)
	}
	if n >= 3 {
		for i := 0; i < n3; i++ {
			for j := 0; j < 2; j++ {
				H(n1+4*i+2*j, n1+1+4*i+2*j, j == 1)
			}
		}
	}
	nn := int(n)
	if n == 5 {
		for i := 0; i < 2; i++ {
			for j := 0; j < 2; j++ {
				B(n0-nn+3-n2*j-4*i, n1+nn-4+n2*j+4*i, 28-16*i+56*j, true)
			}
		}
		for i := 0; i < 2; i++ {
			for j := 0; j < 4; j++ {
				H(n1+n3*j+i, n1+n2-5+n3*j-i, j&1 == 1)
			}
		}
	}
	if n >= 4 {
		lim := 0
		if n == 5 {
			lim = 1
		}
		for i := 0; i <= lim; i++ {
			for j := 0; j < 2; j++ {
				B(n0-nn+2-i-n2*j, n1+nn-3+i+n2*j, 24+48*j, true)
			}
		}
		for i := 0; i <= 2*nn-7; i++ {
			for j := 0; j < 2; j++ {
				H(n1+n2*j+i, n1+n2-1+n2*j-i, j&1 == 1)
			}
		}
	}
	if n >= 3 {
		for i := 0; i < n3; i++ {
			B(n0-n3-1-i, n1+n3+i, 16, true)
		}
	}
	for i := 0; i < n1; i++ {
		H(i, n0-1-i, false)
	}
}

func init() {
	for n := uint(2); n <= 5; n++ {
		buildDCT(n, &dctOps[n])
		p := make([]uint8, 1<<n)
		for i := range p {
			p[i] = uint8(brev(n, i))
		}
		dctPerm[n] = p
	}
}

func round14(x int64) int32 { return int32((x + (1 << 13)) >> 14) }

// idct1d performs the in-place inverse DCT of t (length 1<<n).
func idct1d(t []int32, n uint) {
	var c [32]int32
	copy(c[:1<<n], t)
	perm := dctPerm[n]
	for i := range perm {
		t[i] = c[perm[i]]
	}
	for _, op := range dctOps[n] {
		a, b := op.a, op.b
		if op.kind == 1 {
			x, y := t[a], t[b]
			t[a] = x + y
			t[b] = x - y
			continue
		}
		x := int64(t[a])*op.cs - int64(t[b])*op.sn
		y := int64(t[a])*op.sn + int64(t[b])*op.cs
		if op.flip {
			t[a], t[b] = round14(y), round14(x)
		} else {
			t[a], t[b] = round14(x), round14(y)
		}
	}
}

const (
	sinpi19 = 5283
	sinpi29 = 9929
	sinpi39 = 13377
	sinpi49 = 15212
)

func iadst4(t []int32) {
	s0 := int64(sinpi19) * int64(t[0])
	s1 := int64(sinpi29) * int64(t[0])
	s2 := int64(sinpi39) * int64(t[1])
	s3 := int64(sinpi49) * int64(t[2])
	s4 := int64(sinpi19) * int64(t[2])
	s5 := int64(sinpi29) * int64(t[3])
	s6 := int64(sinpi49) * int64(t[3])
	v := int64(t[0]) - int64(t[2]) + int64(t[3])
	s7 := int64(sinpi39) * v
	x0 := s0 + s3 + s5
	x1 := s1 - s4 - s6
	x2 := s7
	x3 := s2
	t[0] = round14(x0 + x3)
	t[1] = round14(x1 + x3)
	t[2] = round14(x2)
	t[3] = round14(x0 + x1 - x3)
}

type adstCtx struct {
	t []int32
	s [16]int64
}

func (c *adstCtx) B(a, b, angle int, flip bool) {
	t := c.t
	x := int64(t[a])*cos64(angle) - int64(t[b])*sin64(angle)
	y := int64(t[a])*sin64(angle) + int64(t[b])*cos64(angle)
	if flip {
		t[a], t[b] = round14(y), round14(x)
	} else {
		t[a], t[b] = round14(x), round14(y)
	}
}

func (c *adstCtx) H(a, b int) {
	x, y := c.t[a], c.t[b]
	c.t[a] = x + y
	c.t[b] = x - y
}

func (c *adstCtx) SB(a, b, angle int, flip bool) {
	t := c.t
	x := int64(t[a])*cos64(angle) - int64(t[b])*sin64(angle)
	y := int64(t[a])*sin64(angle) + int64(t[b])*cos64(angle)
	if flip {
		c.s[a], c.s[b] = y, x
	} else {
		c.s[a], c.s[b] = x, y
	}
}

func (c *adstCtx) SH(a, b int) {
	x, y := c.s[a], c.s[b]
	c.t[a] = round14(x + y)
	c.t[b] = round14(x - y)
}

func (c *adstCtx) inPerm(n uint) {
	n0 := 1 << n
	n1 := n0 >> 1
	var cp [16]int32
	copy(cp[:n0], c.t)
	for i := 0; i < n1; i++ {
		c.t[2*i] = cp[n0-1-2*i]
		c.t[2*i+1] = cp[2*i]
	}
}

func (c *adstCtx) outPerm(n uint) {
	var cp [16]int32
	copy(cp[:1<<n], c.t)
	if n == 4 {
		for a := 0; a < 2; a++ {
			for b := 0; b < 2; b++ {
				for cc := 0; cc < 2; cc++ {
					for d := 0; d < 2; d++ {
						c.t[8*a+4*b+2*cc+d] = cp[8*(d^cc)+4*(cc^b)+2*(b^a)+a]
					}
				}
			}
		}
		return
	}
	for a := 0; a < 2; a++ {
		for b := 0; b < 2; b++ {
			for cc := 0; cc < 2; cc++ {
				c.t[4*a+2*b+cc] = cp[4*(cc^b)+2*(b^a)+a]
			}
		}
	}
}

func iadst8(t []int32) {
	c := adstCtx{t: t}
	c.inPerm(3)
	for i := 0; i < 4; i++ {
		c.SB(2*i, 1+2*i, 30-8*i, true)
	}
	for i := 0; i < 4; i++ {
		c.SH(i, 4+i)
	}
	for i := 0; i < 2; i++ {
		c.SB(4+3*i, 5+i, 24-16*i, true)
	}
	for i := 0; i < 2; i++ {
		c.SH(4+i, 6+i)
	}
	for i := 0; i < 2; i++ {
		c.H(i, 2+i)
	}
	for i := 0; i < 2; i++ {
		c.B(2+4*i, 3+4*i, 16, true)
	}
	c.outPerm(3)
	for i := 0; i < 4; i++ {
		t[1+2*i] = -t[1+2*i]
	}
}

func iadst16(t []int32) {
	c := adstCtx{t: t}
	c.inPerm(4)
	for i := 0; i < 8; i++ {
		c.SB(2*i, 1+2*i, 31-4*i, true)
	}
	for i := 0; i < 8; i++ {
		c.SH(i, 8+i)
	}
	for i := 0; i < 4; i++ {
		c.SB(8+2*i, 9+2*i, 28-16*i, true)
	}
	for i := 0; i < 4; i++ {
		c.SH(8+i, 12+i)
	}
	for i := 0; i < 4; i++ {
		c.H(i, 4+i)
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			c.SB(4+8*i+3*j, 5+8*i+j, 24-16*j, true)
		}
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			c.SH(4+8*j+i, 6+8*j+i)
		}
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			c.H(8*j+i, 2+8*j+i)
		}
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			c.B(2+4*j+8*i, 3+4*j+8*i, 48+64*(i^j), false)
		}
	}
	c.outPerm(4)
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			t[1+12*j+2*i] = -t[1+12*j+2*i]
		}
	}
}

func iadst1d(t []int32, n uint) {
	switch n {
	case 2:
		iadst4(t)
	case 3:
		iadst8(t)
	default:
		iadst16(t)
	}
}

func iwht1d(t []int32, shift uint) {
	a := t[0] >> shift
	c := t[1] >> shift
	d := t[2] >> shift
	b := t[3] >> shift
	a += c
	d -= b
	e := (a - d) >> 1
	b = e - b
	c = e - c
	a -= b
	d += c
	t[0], t[1], t[2], t[3] = a, b, c, d
}

// inverseTransform2D transforms the n0 x n0 block in buf (row major) in
// place. rowNZ has bit i set when row i has non-zero input.
func inverseTransform2D(buf []int32, n uint, txType int, lossless bool, rowMax int) {
	n0 := 1 << n
	var t [32]int32
	for i := 0; i <= rowMax && i < n0; i++ {
		row := buf[i*n0 : i*n0+n0]
		nz := false
		for _, v := range row {
			if v != 0 {
				nz = true
				break
			}
		}
		if !nz {
			continue
		}
		copy(t[:n0], row)
		switch {
		case lossless:
			iwht1d(t[:], 2)
		case txType == dctDct || txType == adstDct:
			idct1d(t[:n0], n)
		default:
			iadst1d(t[:n0], n)
		}
		copy(row, t[:n0])
	}
	shift := uint(n + 2)
	if shift > 6 {
		shift = 6
	}
	for j := 0; j < n0; j++ {
		for i := 0; i < n0; i++ {
			t[i] = buf[i*n0+j]
		}
		switch {
		case lossless:
			iwht1d(t[:], 0)
		case txType == dctDct || txType == dctAdst:
			idct1d(t[:n0], n)
		default:
			iadst1d(t[:n0], n)
		}
		if lossless {
			for i := 0; i < n0; i++ {
				buf[i*n0+j] = t[i]
			}
		} else {
			for i := 0; i < n0; i++ {
				buf[i*n0+j] = (t[i] + (1 << (shift - 1))) >> shift
			}
		}
	}
}
