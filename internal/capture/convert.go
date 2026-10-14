package capture

import "fmt"

// PixelFormat names a raw camera layout that Convert understands. Names follow the V4L2 / Media Foundation usage
// (YUYV == YUY2, YU12 == I420).
type PixelFormat string

const (
	PixYUYV  PixelFormat = "YUYV" // packed 4:2:2, Y0 U Y1 V
	PixUYVY  PixelFormat = "UYVY" // packed 4:2:2, U Y0 V Y1
	PixNV12  PixelFormat = "NV12" // Y plane, then interleaved U V at half resolution
	PixNV21  PixelFormat = "NV21" // like NV12 with V first
	PixI420  PixelFormat = "I420" // planar Y, U, V (V4L2 YU12)
	PixYV12  PixelFormat = "YV12" // planar Y, V, U
	PixRGB24 PixelFormat = "RGB24"
	PixBGR24 PixelFormat = "BGR24"
	PixGrey  PixelFormat = "GREY"
)

// ConvertSize reports the number of source bytes Convert needs for a w x h frame whose luma row pitch is stride
// bytes (for packed formats stride is the pitch of one full row; for planar formats the pitch of the Y plane).
func ConvertSize(f PixelFormat, w, h, stride int) int {
	switch f {
	case PixYUYV, PixUYVY, PixRGB24, PixBGR24, PixGrey:
		return stride * h
	case PixNV12, PixNV21:
		return stride*h + stride*((h+1)/2)
	case PixI420, PixYV12:
		cs := (stride + 1) / 2
		return stride*h + 2*cs*((h+1)/2)
	}
	return -1
}

// Convert converts src to I420 into dst, which must already be dst.W x dst.H. stride is the source row pitch in
// bytes. Every conversion uses the BT.601 limited-range matrix for RGB and the identity for YUV sources, so the
// result of a YUV format is bit-exact with the data the camera produced (apart from the chroma averaging that
// 4:2:2 to 4:2:0 needs).
func Convert(dst *Frame, f PixelFormat, src []byte, stride int) error {
	w, h := dst.W, dst.H
	if stride <= 0 {
		return fmt.Errorf("capture: bad stride %d", stride)
	}
	need := ConvertSize(f, w, h, stride)
	if need < 0 {
		return fmt.Errorf("capture: unsupported pixel format %q", f)
	}
	if len(src) < need {
		return fmt.Errorf("capture: short %s buffer: %d bytes, need %d for %dx%d", f, len(src), need, w, h)
	}
	if (f == PixYUYV || f == PixUYVY) && w&1 == 1 {
		return fmt.Errorf("capture: %s needs an even width, got %d", f, w)
	}
	switch f {
	case PixYUYV:
		packed422(dst, src, stride, 0, 1, 2, 3)
	case PixUYVY:
		packed422(dst, src, stride, 1, 0, 3, 2)
	case PixNV12:
		semiPlanar(dst, src, stride, false)
	case PixNV21:
		semiPlanar(dst, src, stride, true)
	case PixI420:
		planar(dst, src, stride, false)
	case PixYV12:
		planar(dst, src, stride, true)
	case PixRGB24:
		rgb24(dst, src, stride, 0, 1, 2)
	case PixBGR24:
		rgb24(dst, src, stride, 2, 1, 0)
	case PixGrey:
		grey(dst, src, stride)
	}
	return nil
}

// packed422 handles YUYV (y0=0,u=1,y1=2,v=3) and UYVY (u=0,y0=1,v=2,y1=3). Vertical pairs of chroma rows are averaged.
func packed422(d *Frame, s []byte, stride, y0, u, y1, v int) {
	w, h := d.W, d.H
	for y := 0; y < h; y++ {
		row := s[y*stride:]
		yo := d.Y[y*d.YStride:]
		for x := 0; x+1 < w; x += 2 {
			p := row[x*2:]
			yo[x], yo[x+1] = p[y0], p[y1]
		}
		if w&1 == 1 {
			yo[w-1] = row[(w-1)*2+y0]
		}
	}
	for y := 0; y < h; y += 2 {
		r0 := s[y*stride:]
		r1 := r0
		if y+1 < h {
			r1 = s[(y+1)*stride:]
		}
		uo := d.U[(y/2)*d.UVStride:]
		vo := d.V[(y/2)*d.UVStride:]
		for x := 0; x < (w+1)/2; x++ {
			o := x * 4
			uo[x] = uint8((int(r0[o+u]) + int(r1[o+u]) + 1) >> 1)
			vo[x] = uint8((int(r0[o+v]) + int(r1[o+v]) + 1) >> 1)
		}
	}
}

func copyPlane(dst []byte, ds int, src []byte, ss, w, h int) {
	for y := 0; y < h; y++ {
		copy(dst[y*ds:y*ds+w], src[y*ss:y*ss+w])
	}
}

func semiPlanar(d *Frame, s []byte, stride int, swap bool) {
	w, h := d.W, d.H
	copyPlane(d.Y, d.YStride, s, stride, w, h)
	uv := s[stride*h:]
	cw, ch := (w+1)/2, (h+1)/2
	a, b := 0, 1
	if swap {
		a, b = 1, 0
	}
	for y := 0; y < ch; y++ {
		row := uv[y*stride:]
		uo := d.U[y*d.UVStride:]
		vo := d.V[y*d.UVStride:]
		for x := 0; x < cw; x++ {
			uo[x], vo[x] = row[x*2+a], row[x*2+b]
		}
	}
}

func planar(d *Frame, s []byte, stride int, swap bool) {
	w, h := d.W, d.H
	copyPlane(d.Y, d.YStride, s, stride, w, h)
	cs := (stride + 1) / 2
	cw, ch := (w+1)/2, (h+1)/2
	p1 := s[stride*h:]
	p2 := p1[cs*ch:]
	if swap {
		p1, p2 = p2, p1
	}
	copyPlane(d.U, d.UVStride, p1, cs, cw, ch)
	copyPlane(d.V, d.UVStride, p2, cs, cw, ch)
}

func rgb24(d *Frame, s []byte, stride, ri, gi, bi int) {
	w, h := d.W, d.H
	for y := 0; y < h; y += 2 {
		for x := 0; x < w; x += 2 {
			var sr, sg, sb, n int
			for dy := 0; dy < 2 && y+dy < h; dy++ {
				row := s[(y+dy)*stride:]
				for dx := 0; dx < 2 && x+dx < w; dx++ {
					p := row[(x+dx)*3:]
					r, g, b := int(p[ri]), int(p[gi]), int(p[bi])
					d.Y[(y+dy)*d.YStride+x+dx] = RGBToY(r, g, b)
					sr += r
					sg += g
					sb += b
					n++
				}
			}
			d.U[(y/2)*d.UVStride+x/2] = RGBToU(sr/n, sg/n, sb/n)
			d.V[(y/2)*d.UVStride+x/2] = RGBToV(sr/n, sg/n, sb/n)
		}
	}
}

func grey(d *Frame, s []byte, stride int) {
	for y := 0; y < d.H; y++ {
		row := s[y*stride:]
		yo := d.Y[y*d.YStride:]
		for x := 0; x < d.W; x++ {
			yo[x] = uint8(16 + (int(row[x])*219+127)/255)
		}
	}
	for i := range d.U {
		d.U[i], d.V[i] = 128, 128
	}
}
