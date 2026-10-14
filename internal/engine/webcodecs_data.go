package engine

// webcodecs_data.go: the pixel and sample storage behind VideoFrame and AudioData.
//
// A VideoFrame or AudioData object in JavaScript is a handle into this process-wide table; the pixels never enter
// the JS heap. The payload (wcData) is reference counted, so VideoFrame.clone() shares it and close() on one handle
// leaves the others alone, and a frame posted to a worker moves by token (webcodecs.go). Planes are tightly packed
// (row stride = row bytes) in the format the frame was made with; conversion happens on copyTo, on drawing to a
// canvas and on encoding.

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
)

// wcMaxLive bounds the bytes of all open frames and audio data in the process.
const wcMaxLive = 2 << 30

var (
	wcHandles sync.Map // int64 -> *wcHandle
	wcSeq     atomic.Int64
	wcLive    atomic.Int64
)

type wcData struct {
	refs  atomic.Int32
	audio bool
	bytes int64

	// video
	format  string
	w, h    int // coded size
	planes  [][]byte
	strides []int
	matrix  string // "bt709", "smpte170m", "bt2020-ncl", "rgb" or "" (derived from the height)
	full    bool
	prim    string
	trc     string

	// audio: planar float32 whatever the format of the AudioData object
	rate, nch, frames int
	pcm               [][]float32
}

type wcHandle struct {
	id             int64
	d              *wcData
	vx, vy, vw, vh int // visible rectangle
	owner          *wcHost
}

func (d *wcData) release() {
	if d.refs.Add(-1) == 0 {
		wcLive.Add(-d.bytes)
		d.planes, d.pcm = nil, nil
	}
}

// wcNewHandle registers a payload for a VM. It returns nil when the process is over its frame memory budget.
func wcNewHandle(owner *wcHost, d *wcData, vx, vy, vw, vh int) *wcHandle {
	if d.refs.Load() == 0 { // a fresh payload: count it
		if wcLive.Add(d.bytes) > wcMaxLive {
			wcLive.Add(-d.bytes)
			return nil
		}
	}
	d.refs.Add(1)
	h := &wcHandle{id: wcSeq.Add(1), d: d, vx: vx, vy: vy, vw: vw, vh: vh, owner: owner}
	wcHandles.Store(h.id, h)
	owner.own(h.id)
	return h
}

func wcLookup(id int64) *wcHandle {
	if v, ok := wcHandles.Load(id); ok {
		return v.(*wcHandle)
	}
	return nil
}

// ---- layouts ----------------------------------------------------------------------------------------------

type wcPlane struct {
	xdiv, ydiv, bpp int // horizontal and vertical subsampling, bytes per sample position
}

func wcFormatPlanes(format string) []wcPlane {
	switch format {
	case "I420":
		return []wcPlane{{1, 1, 1}, {2, 2, 1}, {2, 2, 1}}
	case "I420A":
		return []wcPlane{{1, 1, 1}, {2, 2, 1}, {2, 2, 1}, {1, 1, 1}}
	case "I422":
		return []wcPlane{{1, 1, 1}, {2, 1, 1}, {2, 1, 1}}
	case "I444":
		return []wcPlane{{1, 1, 1}, {1, 1, 1}, {1, 1, 1}}
	case "NV12":
		return []wcPlane{{1, 1, 1}, {2, 2, 2}}
	case "RGBA", "RGBX", "BGRA", "BGRX":
		return []wcPlane{{1, 1, 4}}
	}
	return nil
}

func wcIsRGB(format string) bool {
	switch format {
	case "RGBA", "RGBX", "BGRA", "BGRX":
		return true
	}
	return false
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// rowBytes and rows of plane p of a w x h area.
func (p wcPlane) dims(w, h int) (rowBytes, rows int) {
	return ceilDiv(w, p.xdiv) * p.bpp, ceilDiv(h, p.ydiv)
}

type wcLayout struct {
	Offset int `json:"offset"`
	Stride int `json:"stride"`
}

// wcTight is the default layout of format for a w x h area, and the bytes it takes.
func wcTight(format string, w, h int) ([]wcLayout, int) {
	var out []wcLayout
	off := 0
	for _, p := range wcFormatPlanes(format) {
		rb, rows := p.dims(w, h)
		out = append(out, wcLayout{off, rb})
		off += rb * rows
	}
	return out, off
}

// wcCheckLayout validates a user layout against a w x h area and returns the bytes it spans.
func wcCheckLayout(format string, w, h int, l []wcLayout) (int, error) {
	ps := wcFormatPlanes(format)
	if len(l) != len(ps) {
		return 0, fmt.Errorf("TypeError:Invalid layout: %d planes given, %s has %d", len(l), format, len(ps))
	}
	end := 0
	for i, p := range ps {
		rb, rows := p.dims(w, h)
		if l[i].Offset < 0 || l[i].Stride < rb {
			return 0, fmt.Errorf("TypeError:Invalid layout: plane %d stride %d is smaller than the row size %d", i, l[i].Stride, rb)
		}
		if e := l[i].Offset + l[i].Stride*(rows-1) + rb; rows > 0 && e > end {
			end = e
		}
	}
	// planes must not overlap
	for i := range ps {
		for j := i + 1; j < len(ps); j++ {
			ri, rowsi := ps[i].dims(w, h)
			rj, rowsj := ps[j].dims(w, h)
			ei := l[i].Offset + l[i].Stride*(rowsi-1) + ri
			ej := l[j].Offset + l[j].Stride*(rowsj-1) + rj
			if l[i].Offset < ej && l[j].Offset < ei {
				return 0, fmt.Errorf("TypeError:Invalid layout: planes overlap")
			}
		}
	}
	return end, nil
}

// wcFromBytes builds a payload from source bytes laid out as l (nil = tight).
func wcFromBytes(format string, w, h int, src []byte, l []wcLayout) (*wcData, error) {
	ps := wcFormatPlanes(format)
	if ps == nil {
		return nil, fmt.Errorf("TypeError:Unsupported pixel format %q", format)
	}
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
		return nil, fmt.Errorf("TypeError:Invalid coded size %dx%d", w, h)
	}
	tight, total := wcTight(format, w, h)
	need := total
	if l != nil {
		var err error
		if need, err = wcCheckLayout(format, w, h, l); err != nil {
			return nil, err
		}
	} else {
		l = tight
	}
	if len(src) < need {
		return nil, fmt.Errorf("TypeError:The data is too small (%d bytes, %d needed)", len(src), need)
	}
	d := &wcData{format: format, w: w, h: h, bytes: int64(total)}
	for i, p := range ps {
		rb, rows := p.dims(w, h)
		buf := make([]byte, rb*rows)
		for r := 0; r < rows; r++ {
			copy(buf[r*rb:(r+1)*rb], src[l[i].Offset+r*l[i].Stride:])
		}
		d.planes = append(d.planes, buf)
		d.strides = append(d.strides, rb)
	}
	return d, nil
}

// wcCopyRect copies the rectangle (x,y,w,h) of the frame, in its own format, into dst laid out as l.
func (d *wcData) copyRect(x, y, w, h int, dst []byte, l []wcLayout) error {
	for i, p := range wcFormatPlanes(d.format) {
		if x%p.xdiv != 0 || y%p.ydiv != 0 {
			return fmt.Errorf("TypeError:The rect is not aligned to the chroma subsampling of %s", d.format)
		}
		rb, rows := p.dims(w, h)
		sx, sy := x/p.xdiv*p.bpp, y/p.ydiv
		for r := 0; r < rows; r++ {
			copy(dst[l[i].Offset+r*l[i].Stride:l[i].Offset+r*l[i].Stride+rb], d.planes[i][(sy+r)*d.strides[i]+sx:])
		}
	}
	return nil
}

// ---- colour -------------------------------------------------------------------------------------------------

type wcCoef struct{ rv, gu, gv, bu int32 } // 16.16 multipliers of Cr/Cb

func wcMatrix(matrix string, h int) (c wcCoef) {
	switch matrix {
	case "bt709", "bt2020-ncl":
		return wcCoef{117489, 13975, 34925, 138438}
	case "smpte170m", "bt470bg":
		return wcCoef{104597, 25675, 53279, 132201}
	}
	if h >= 720 {
		return wcCoef{117489, 13975, 34925, 138438}
	}
	return wcCoef{104597, 25675, 53279, 132201}
}

func clamp8(v int32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// toRGBA converts the rectangle to straight-alpha RGBA, row after row, w*4 bytes per row.
func (d *wcData) toRGBA(x0, y0, w, h int) []byte {
	out := make([]byte, w*h*4)
	if wcIsRGB(d.format) {
		sw := func(i int, src []byte) (r, g, b, a byte) {
			switch d.format {
			case "RGBA":
				return src[i], src[i+1], src[i+2], src[i+3]
			case "RGBX":
				return src[i], src[i+1], src[i+2], 255
			case "BGRA":
				return src[i+2], src[i+1], src[i], src[i+3]
			}
			return src[i+2], src[i+1], src[i], 255
		}
		for j := 0; j < h; j++ {
			row := d.planes[0][(y0+j)*d.strides[0]+x0*4:]
			for i := 0; i < w; i++ {
				r, g, b, a := sw(i*4, row)
				o := (j*w + i) * 4
				out[o], out[o+1], out[o+2], out[o+3] = r, g, b, a
			}
		}
		return out
	}
	c := wcMatrix(d.matrix, d.h)
	ps := wcFormatPlanes(d.format)
	var ys, rngScale int32 = 16, 76309
	if d.full { // full range: no luma offset or scale, chroma multipliers 224/255 of the limited-range ones
		ys, rngScale = 0, 65536
		c = wcCoef{c.rv * 224 / 255, c.gu * 224 / 255, c.gv * 224 / 255, c.bu * 224 / 255}
	}
	for j := 0; j < h; j++ {
		yy := y0 + j
		yrow := d.planes[0][yy*d.strides[0]:]
		cy := yy / ps[1].ydiv
		for i := 0; i < w; i++ {
			xx := x0 + i
			var cb, cr int32
			if d.format == "NV12" {
				o := cy*d.strides[1] + (xx/2)*2
				cb, cr = int32(d.planes[1][o]), int32(d.planes[1][o+1])
			} else {
				cx := xx / ps[1].xdiv
				cb, cr = int32(d.planes[1][cy*d.strides[1]+cx]), int32(d.planes[2][cy*d.strides[2]+cx])
			}
			cb -= 128
			cr -= 128
			l := (int32(yrow[xx]) - ys) * rngScale
			o := (j*w + i) * 4
			out[o] = clamp8((l + c.rv*cr + 32768) >> 16)
			out[o+1] = clamp8((l - c.gu*cb - c.gv*cr + 32768) >> 16)
			out[o+2] = clamp8((l + c.bu*cb + 32768) >> 16)
			out[o+3] = 255
			if d.format == "I420A" {
				out[o+3] = d.planes[3][yy*d.strides[3]+xx]
			}
		}
	}
	return out
}

// toI420 converts the rectangle to tightly packed I420 (BT.601 limited range below 720 lines, BT.709 above), the
// form the VP8 encoder takes.
func (d *wcData) toI420(x0, y0, w, h int) (y, u, v []byte) {
	cw, chh := ceilDiv(w, 2), ceilDiv(h, 2)
	y = make([]byte, w*h)
	u = make([]byte, cw*chh)
	v = make([]byte, cw*chh)
	if d.format == "I420" || d.format == "I420A" {
		for j := 0; j < h; j++ {
			copy(y[j*w:(j+1)*w], d.planes[0][(y0+j)*d.strides[0]+x0:])
		}
		for j := 0; j < chh; j++ {
			copy(u[j*cw:(j+1)*cw], d.planes[1][(y0/2+j)*d.strides[1]+x0/2:])
			copy(v[j*cw:(j+1)*cw], d.planes[2][(y0/2+j)*d.strides[2]+x0/2:])
		}
		return
	}
	rgba := d.toRGBA(x0, y0, w, h)
	hd := h >= 720
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			o := (j*w + i) * 4
			r, g, b := int32(rgba[o]), int32(rgba[o+1]), int32(rgba[o+2])
			if hd {
				y[j*w+i] = clamp8((187*r+629*g+63*b+512)>>10 + 16)
			} else {
				y[j*w+i] = clamp8((263*r+516*g+100*b+512)>>10 + 16)
			}
		}
	}
	for j := 0; j < chh; j++ {
		for i := 0; i < cw; i++ {
			var r, g, b, n int32
			for dj := 0; dj < 2; dj++ {
				for di := 0; di < 2; di++ {
					jj, ii := min(2*j+dj, h-1), min(2*i+di, w-1)
					o := (jj*w + ii) * 4
					r += int32(rgba[o])
					g += int32(rgba[o+1])
					b += int32(rgba[o+2])
					n++
				}
			}
			r, g, b = r/n, g/n, b/n
			if hd {
				u[j*cw+i] = clamp8((-103*r-347*g+450*b+512)>>10 + 128)
				v[j*cw+i] = clamp8((450*r-409*g-41*b+512)>>10 + 128)
			} else {
				u[j*cw+i] = clamp8((-152*r-298*g+450*b+512)>>10 + 128)
				v[j*cw+i] = clamp8((450*r-377*g-73*b+512)>>10 + 128)
			}
		}
	}
	return
}

// fromRGBA builds an RGBA payload from straight-alpha rows.
func wcFromRGBA(pix []byte, w, h int) *wcData {
	return &wcData{format: "RGBA", w: w, h: h, planes: [][]byte{pix}, strides: []int{w * 4}, bytes: int64(len(pix)), matrix: "rgb"}
}

// convertRGB writes the rectangle as the RGB format `to` (RGBA, RGBX, BGRA, BGRX), rows w*4 bytes, into dst laid
// out by l.
func (d *wcData) convertRGB(x, y, w, h int, to string, dst []byte, l wcLayout) {
	rgba := d.toRGBA(x, y, w, h)
	for j := 0; j < h; j++ {
		row := dst[l.Offset+j*l.Stride:]
		for i := 0; i < w; i++ {
			o := (j*w + i) * 4
			r, g, b, a := rgba[o], rgba[o+1], rgba[o+2], rgba[o+3]
			if to == "RGBX" || to == "BGRX" {
				a = 255
			}
			if to[0] == 'B' {
				r, b = b, r
			}
			row[i*4], row[i*4+1], row[i*4+2], row[i*4+3] = r, g, b, a
		}
	}
}

// ---- audio -------------------------------------------------------------------------------------------------

type wcAudioFormat struct {
	bytes  int
	planar bool
	kind   byte // 'u' unsigned 8, 's' signed int, 'f' float
}

var wcAudioFormats = map[string]wcAudioFormat{
	"u8": {1, false, 'u'}, "s16": {2, false, 's'}, "s32": {4, false, 's'}, "f32": {4, false, 'f'},
	"u8-planar": {1, true, 'u'}, "s16-planar": {2, true, 's'}, "s32-planar": {4, true, 's'}, "f32-planar": {4, true, 'f'},
}

func wcSampleOut(f wcAudioFormat, v float32, dst []byte) {
	if v > 1 {
		v = 1
	}
	if v < -1 {
		v = -1
	}
	switch {
	case f.kind == 'u':
		dst[0] = byte(math.Round(float64(v)*127.5 + 127.5))
	case f.kind == 's' && f.bytes == 2:
		x := int16(math.Round(float64(v) * 32767))
		dst[0], dst[1] = byte(x), byte(x>>8)
	case f.kind == 's':
		x := int32(math.Round(float64(v) * 2147483647))
		dst[0], dst[1], dst[2], dst[3] = byte(x), byte(x>>8), byte(x>>16), byte(x>>24)
	default:
		b := math.Float32bits(v)
		dst[0], dst[1], dst[2], dst[3] = byte(b), byte(b>>8), byte(b>>16), byte(b>>24)
	}
}

func wcSampleIn(f wcAudioFormat, src []byte) float32 {
	switch {
	case f.kind == 'u':
		return (float32(src[0]) - 127.5) / 127.5
	case f.kind == 's' && f.bytes == 2:
		return float32(int16(uint16(src[0])|uint16(src[1])<<8)) / 32768
	case f.kind == 's':
		return float32(int32(uint32(src[0])|uint32(src[1])<<8|uint32(src[2])<<16|uint32(src[3])<<24)) / 2147483648
	}
	return math.Float32frombits(uint32(src[0]) | uint32(src[1])<<8 | uint32(src[2])<<16 | uint32(src[3])<<24)
}

func wcAudioFromBytes(format string, rate, ch, frames int, src []byte) (*wcData, error) {
	f, ok := wcAudioFormats[format]
	if !ok {
		return nil, fmt.Errorf("TypeError:Unsupported audio sample format %q", format)
	}
	if rate <= 0 || ch <= 0 || ch > 256 || frames <= 0 || frames > 1<<26 {
		return nil, fmt.Errorf("TypeError:Invalid audio parameters")
	}
	if len(src) < frames*ch*f.bytes {
		return nil, fmt.Errorf("TypeError:The data is too small for %d frames of %d channels", frames, ch)
	}
	d := &wcData{audio: true, format: format, rate: rate, nch: ch, frames: frames, bytes: int64(frames * ch * 4)}
	d.pcm = make([][]float32, ch)
	for c := range d.pcm {
		d.pcm[c] = make([]float32, frames)
		for i := 0; i < frames; i++ {
			var o int
			if f.planar {
				o = (c*frames + i) * f.bytes
			} else {
				o = (i*ch + c) * f.bytes
			}
			d.pcm[c][i] = wcSampleIn(f, src[o:])
		}
	}
	return d, nil
}

// audioFromPlanar wraps decoder output (copied) as f32-planar data.
func wcAudioFromPlanar(pcm [][]float32, rate int) *wcData {
	frames := 0
	if len(pcm) > 0 {
		frames = len(pcm[0])
	}
	d := &wcData{audio: true, format: "f32-planar", rate: rate, nch: len(pcm), frames: frames, bytes: int64(frames * len(pcm) * 4)}
	for _, c := range pcm {
		d.pcm = append(d.pcm, append([]float32(nil), c...))
	}
	return d
}

// size of frameCount frames of one plane (or all channels when interleaved) in format.
func (d *wcData) audioSize(format string, planeIndex, frameOffset, frameCount int) (int, error) {
	f, ok := wcAudioFormats[format]
	if !ok {
		return 0, fmt.Errorf("TypeError:Unsupported audio sample format %q", format)
	}
	if frameOffset < 0 || frameOffset > d.frames {
		return 0, fmt.Errorf("RangeError:frameOffset is out of range")
	}
	if frameCount < 0 {
		frameCount = d.frames - frameOffset
	}
	if frameOffset+frameCount > d.frames {
		return 0, fmt.Errorf("RangeError:frameOffset + frameCount is out of range")
	}
	if f.planar {
		if planeIndex < 0 || planeIndex >= d.nch {
			return 0, fmt.Errorf("RangeError:planeIndex is out of range")
		}
		return frameCount * f.bytes, nil
	}
	if planeIndex != 0 {
		return 0, fmt.Errorf("RangeError:planeIndex is out of range")
	}
	return frameCount * d.nch * f.bytes, nil
}

func (d *wcData) audioCopy(format string, planeIndex, frameOffset, frameCount int, dst []byte) {
	f := wcAudioFormats[format]
	if frameCount < 0 {
		frameCount = d.frames - frameOffset
	}
	if f.planar {
		for i := 0; i < frameCount; i++ {
			wcSampleOut(f, d.pcm[planeIndex][frameOffset+i], dst[i*f.bytes:])
		}
		return
	}
	for i := 0; i < frameCount; i++ {
		for c := 0; c < d.nch; c++ {
			wcSampleOut(f, d.pcm[c][frameOffset+i], dst[(i*d.nch+c)*f.bytes:])
		}
	}
}

func wcLower(s string) string { return strings.ToLower(s) }
