package vp8enc

import "github.com/sweefbrowser/sweef/internal/media/vp8"

// plane is one 8-bit image plane padded by a border on every side, so that
// motion compensation can read outside the picture without clamping.
type plane struct {
	pix    []byte
	stride int
	off    int // index of pixel (0,0)
	w, h   int // macroblock-aligned size
	border int
}

func newPlane(w, h, border int) *plane {
	stride := w + 2*border
	return &plane{pix: make([]byte, stride*(h+2*border)), stride: stride, off: border*stride + border, w: w, h: h, border: border}
}

func (p *plane) idx(x, y int) int { return p.off + y*p.stride + x }

// extend replicates the edge pixels into the border.
func (p *plane) extend() {
	b := p.border
	for y := 0; y < p.h; y++ {
		row := p.pix[p.idx(0, y)-b : p.idx(0, y)+p.w+b]
		l, r := row[b], row[b+p.w-1]
		for i := 0; i < b; i++ {
			row[i] = l
			row[b+p.w+i] = r
		}
	}
	top := p.pix[p.idx(-b, 0) : p.idx(-b, 0)+p.stride]
	bot := p.pix[p.idx(-b, p.h-1) : p.idx(-b, p.h-1)+p.stride]
	for i := 1; i <= b; i++ {
		copy(p.pix[p.idx(-b, -i):], top)
		copy(p.pix[p.idx(-b, p.h-1+i):], bot)
	}
}

// intraBorders sets the values VP8 intra prediction assumes outside the picture:
// 127 above (including the corner) and 129 to the left.
func (p *plane) intraBorders() {
	for x := -1; x <= p.w+4; x++ {
		p.pix[p.idx(x, -1)] = 127
	}
	for y := 0; y < p.h; y++ {
		p.pix[p.idx(-1, y)] = 129
	}
}

// frame is a reconstructed picture (three padded planes).
type frame struct {
	y, u, v *plane
	refs    int
}

func newFrame(w, h int) *frame {
	return &frame{y: newPlane(w, h, 32), u: newPlane(w/2, h/2, 16), v: newPlane(w/2, h/2, 16)}
}

func (f *frame) extend() { f.y.extend(); f.u.extend(); f.v.extend() }

// Intra prediction modes of 16x16 luma and 8x8 chroma blocks.
const (
	predDC = iota
	predV
	predH
	predTM
)

// predictIntra builds the intra prediction of the size x size block whose top-left pixel is (x, y)
// from the already reconstructed neighbours in p (borders must have been set with intraBorders).
func predictIntra(p *plane, x, y, size int, mode int, dst []byte) {
	above := p.pix[p.idx(x, y-1) : p.idx(x, y-1)+size]
	switch mode {
	case predDC:
		sum := 0
		shift := 0
		if y > 0 {
			for _, v := range above {
				sum += int(v)
			}
			shift++
		}
		if x > 0 {
			i := p.idx(x-1, y)
			for r := 0; r < size; r++ {
				sum += int(p.pix[i])
				i += p.stride
			}
			shift++
		}
		v := byte(128)
		if shift > 0 {
			sh := uint(shift + 2) // 8 samples per edge: shift 1 -> 3 bits, 2 -> 4 bits
			if size == 16 {
				sh = uint(shift + 3)
			}
			v = byte((sum + (1 << (sh - 1))) >> sh)
		}
		for i := 0; i < size*size; i++ {
			dst[i] = v
		}
	case predV:
		for r := 0; r < size; r++ {
			copy(dst[r*size:r*size+size], above)
		}
	case predH:
		i := p.idx(x-1, y)
		for r := 0; r < size; r++ {
			v := p.pix[i]
			row := dst[r*size : r*size+size]
			for c := range row {
				row[c] = v
			}
			i += p.stride
		}
	default: // TM
		tl := int(p.pix[p.idx(x-1, y-1)])
		i := p.idx(x-1, y)
		for r := 0; r < size; r++ {
			d := int(p.pix[i]) - tl
			row := dst[r*size : r*size+size]
			for c := range row {
				v := d + int(above[c])
				if v < 0 {
					v = 0
				} else if v > 255 {
					v = 255
				}
				row[c] = byte(v)
			}
			i += p.stride
		}
	}
}

var sixtap = vp8.SixTap()

func clampPix(v int32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// predictInter builds the w x h motion compensated prediction whose top-left
// pixel in the reference is (x, y) plus the eighth-pel fraction (fx, fy)
// (the decoder's six-tap interpolation). tmp must hold (h+5)*w bytes.
func predictInter(ref *plane, x, y, fx, fy, w, h int, dst []byte, dstStride int, tmp []byte) {
	base := ref.idx(x, y)
	src := ref.pix
	ss := ref.stride
	if fx == 0 && fy == 0 {
		for j := 0; j < h; j++ {
			copy(dst[j*dstStride:j*dstStride+w], src[base+j*ss:base+j*ss+w])
		}
		return
	}
	if fy == 0 {
		f := &sixtap[fx]
		for j := 0; j < h; j++ {
			s := src[base+j*ss-2:]
			o := dst[j*dstStride:]
			for i := 0; i < w; i++ {
				v := int32(s[i])*f[0] + int32(s[i+1])*f[1] + int32(s[i+2])*f[2] + int32(s[i+3])*f[3] + int32(s[i+4])*f[4] + int32(s[i+5])*f[5]
				o[i] = clampPix((v + 64) >> 7)
			}
		}
		return
	}
	if fx == 0 {
		f := &sixtap[fy]
		for j := 0; j < h; j++ {
			o := dst[j*dstStride:]
			b := base + j*ss
			for i := 0; i < w; i++ {
				p := b + i
				v := int32(src[p-2*ss])*f[0] + int32(src[p-ss])*f[1] + int32(src[p])*f[2] + int32(src[p+ss])*f[3] + int32(src[p+2*ss])*f[4] + int32(src[p+3*ss])*f[5]
				o[i] = clampPix((v + 64) >> 7)
			}
		}
		return
	}
	f := &sixtap[fx]
	t := tmp[:(h+5)*w]
	for j := 0; j < h+5; j++ {
		s := src[base+(j-2)*ss-2:]
		o := t[j*w:]
		for i := 0; i < w; i++ {
			v := int32(s[i])*f[0] + int32(s[i+1])*f[1] + int32(s[i+2])*f[2] + int32(s[i+3])*f[3] + int32(s[i+4])*f[4] + int32(s[i+5])*f[5]
			o[i] = clampPix((v + 64) >> 7)
		}
	}
	g := &sixtap[fy]
	for j := 0; j < h; j++ {
		o := dst[j*dstStride:]
		for i := 0; i < w; i++ {
			p := (j+2)*w + i
			v := int32(t[p-2*w])*g[0] + int32(t[p-w])*g[1] + int32(t[p])*g[2] + int32(t[p+w])*g[3] + int32(t[p+2*w])*g[4] + int32(t[p+3*w])*g[5]
			o[i] = clampPix((v + 64) >> 7)
		}
	}
}

// sad16 is the sum of absolute differences of two 16x16 blocks, giving up once it exceeds limit.
func sad16(a []byte, as int, b []byte, bs int, limit int) int {
	sum := 0
	for j := 0; j < 16; j++ {
		x := a[j*as : j*as+16 : j*as+16]
		y := b[j*bs : j*bs+16 : j*bs+16]
		s := 0
		for i := 0; i < 16; i++ {
			d := int(x[i]) - int(y[i])
			if d < 0 {
				d = -d
			}
			s += d
		}
		sum += s
		if sum >= limit {
			return sum
		}
	}
	return sum
}

// sadBlock is the SAD of two w x h blocks.
func sadBlock(a []byte, as int, b []byte, bs int, w, h int) int {
	sum := 0
	for j := 0; j < h; j++ {
		x := a[j*as : j*as+w]
		y := b[j*bs : j*bs+w]
		for i := range x {
			d := int(x[i]) - int(y[i])
			if d < 0 {
				d = -d
			}
			sum += d
		}
	}
	return sum
}
