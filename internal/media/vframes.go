package media

import (
	"fmt"
	"image"
	"runtime"
	"sort"
	"sync"

	"github.com/sweefbrowser/sweef/internal/media/h264"
	"github.com/sweefbrowser/sweef/internal/media/hevc"
	"github.com/sweefbrowser/sweef/internal/media/vp8"
	"github.com/sweefbrowser/sweef/internal/media/vp9"
	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// vdecoder is a pure-Go video decoder fed one access unit at a time. Decode
// returns the picture decoded from that access unit (nil when the unit does
// not produce a displayable picture); pictures are never modified later.
type vdecoder interface {
	Decode(data []byte) (*image.YCbCr, error)
	Reset()
}

// vpic is a decoded picture in either of the two pixel containers: an 8-bit
// 4:2:0 image.YCbCr (the fast path) or a yuv.Frame for every other depth,
// chroma layout or colour description.
type vpic struct {
	ycc *image.YCbCr
	frm *yuv.Frame
}

func (p vpic) valid() bool { return p.ycc != nil || p.frm != nil }

// rgba converts the picture for display.
func (p vpic) rgba(bt709 bool) *image.RGBA {
	if p.frm != nil {
		return p.frm.RGBA()
	}
	return yuvToRGBA(p.ycc, bt709)
}

// picDecoder is implemented by decoders that can return pictures that do not
// fit an 8-bit 4:2:0 image.YCbCr (high bit depth, 4:2:2, 4:4:4, ...). The
// nativeFrames tier prefers it over vdecoder.Decode.
type picDecoder interface {
	DecodePic(data []byte) (vpic, error)
}

// framePic wraps a decoded frame, using the YCbCr fast path when the frame
// is plain 8-bit 4:2:0 with default colour description.
func framePic(fr *yuv.Frame) vpic {
	if fr == nil {
		return vpic{}
	}
	if fr.Depth == 8 && fr.Format == yuv.F420 && !fr.FullRange && (fr.Matrix == yuv.MatrixUnspecified || fr.Matrix == yuv.MatrixBT601 || fr.Matrix == 6 || fr.Matrix == yuv.MatrixBT709) {
		if img, ok := fr.ToYCbCr(); ok {
			return vpic{ycc: img}
		}
	}
	return vpic{frm: fr}
}

type vp8Adapter struct{ d *vp8.Decoder }

func (a *vp8Adapter) Decode(data []byte) (*image.YCbCr, error) { return a.d.Decode(data) }
func (a *vp8Adapter) Reset()                                   { a.d.Reset() }

type vp9Adapter struct{ d *vp9.Decoder }

func (a *vp9Adapter) Decode(data []byte) (*image.YCbCr, error) { return a.d.Decode(data) }
func (a *vp9Adapter) Reset()                                   { a.d.Reset() }
func (a *vp9Adapter) DecodePic(data []byte) (vpic, error) {
	fr, err := a.d.DecodeFrame(data)
	return framePic(fr), err
}

type h264Adapter struct{ d *h264.Decoder }

func (a *h264Adapter) Decode(data []byte) (*image.YCbCr, error) { return a.d.Decode(data) }
func (a *h264Adapter) Reset()                                   { a.d.Reset() }
func (a *h264Adapter) DecodePic(data []byte) (vpic, error) {
	fr, err := a.d.DecodeFrame(data)
	return framePic(fr), err
}

type hevcAdapter struct{ d *hevc.Decoder }

func (a *hevcAdapter) Decode(data []byte) (*image.YCbCr, error) { return a.d.Decode(data) }
func (a *hevcAdapter) Reset()                                   { a.d.Reset() }
func (a *hevcAdapter) DecodePic(data []byte) (vpic, error) {
	fr, err := a.d.DecodeFrame(data)
	return framePic(fr), err
}

// newNativeDecoder returns a decoder for the stream codec, or nil when no
// pure-Go decoder exists for it.
func newNativeDecoder(vs *vstream) vdecoder {
	switch vs.codec {
	case "vp8":
		return &vp8Adapter{vp8.NewDecoder()}
	case "vp9":
		return &vp9Adapter{vp9.NewDecoder()}
	case "h264":
		d := h264.NewDecoder()
		if err := d.SetAVCC(vs.cfg); err != nil {
			return nil
		}
		return &h264Adapter{d}
	case "hevc":
		d := hevc.NewDecoder()
		if err := d.SetHVCC(vs.cfg); err != nil {
			return nil
		}
		return &hevcAdapter{d}
	case "av1":
		return newAV1Adapter(vs.cfg)
	}
	return nil
}

// HasNativeVideo reports whether codec (ffprobe style name) can be decoded
// without ffmpeg.
func HasNativeVideo(codec string) bool {
	switch codec {
	case "vp8", "vp9", "h264", "hevc", "av1":
		return true
	}
	return false
}

// nativeFrames serves frames by timestamp from a pure-Go decoder. It decodes
// forward from the nearest preceding sync sample and remembers its position
// so that sequential playback costs one decode per frame.
type nativeFrames struct {
	src    Source
	vs     *vstream
	dec    vdecoder
	order  []int // sample indices by presentation time
	mu     sync.Mutex
	pos    int // decode index of the last decoded sample (-1 none)
	last   vpic
	cache  map[int]*image.RGBA
	cacheQ []int
	keys   []int // decode indices of sync samples
	hd     bool
	mode   string
	cspace int
}

func newNativeFrames(src Source, info *Info) (*nativeFrames, error) {
	vs, err := loadVideoStream(src, info)
	if err != nil {
		return nil, err
	}
	dec := newNativeDecoder(vs)
	if dec == nil {
		return nil, ErrUnsupported
	}
	nf := &nativeFrames{src: src, vs: vs, dec: dec, pos: -1, cache: map[int]*image.RGBA{}, mode: "native"}
	nf.order = vs.order()
	for i, s := range vs.samples {
		if s.key {
			nf.keys = append(nf.keys, i)
		}
	}
	if len(nf.keys) == 0 || nf.keys[0] != 0 {
		nf.keys = append([]int{0}, nf.keys...)
	}
	// Probe the first picture so unsupported streams (e.g. 4:2:2) fall back to
	// ffmpeg, or fail cleanly, at open time rather than on every frame.
	if _, err := nf.Frame(0); err != nil {
		return nil, err
	}
	return nf, nil
}

func (n *nativeFrames) Mode() string { return n.mode }
func (n *nativeFrames) Close()       {}

func (n *nativeFrames) keyBefore(idx int) int {
	i := sort.SearchInts(n.keys, idx+1)
	return n.keys[i-1]
}

// target returns the decode index of the sample shown at time t.
func (n *nativeFrames) target(t float64) int {
	o := n.order
	i := sort.Search(len(o), func(i int) bool { return n.vs.samples[o[i]].pts > t+1e-6 })
	if i == 0 {
		return o[0]
	}
	return o[i-1]
}

func (n *nativeFrames) Frame(t float64) (image.Image, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	idx := n.target(t)
	if img, ok := n.cache[idx]; ok {
		return img, nil
	}
	start := n.pos + 1
	if n.pos < 0 || idx <= n.pos {
		start = n.keyBefore(idx)
		n.dec.Reset()
		n.last = vpic{}
	} else if k := n.keyBefore(idx); k > n.pos {
		start = k
		n.dec.Reset()
		n.last = vpic{}
	}
	var pic vpic
	pd, _ := n.dec.(picDecoder)
	for i := start; i <= idx; i++ {
		data, err := n.vs.payload(n.src, i)
		if err != nil {
			n.pos = -1
			return nil, err
		}
		var p vpic
		if pd != nil {
			p, err = pd.DecodePic(data)
		} else {
			var ycc *image.YCbCr
			ycc, err = n.dec.Decode(data)
			p = vpic{ycc: ycc}
		}
		if err != nil {
			// A broken access unit: keep going so later key frames recover.
			if i == idx && !n.last.valid() {
				n.pos = -1
				return nil, fmt.Errorf("media: %s decode: %w", n.vs.codec, err)
			}
			continue
		}
		pic = p
		if p.valid() {
			n.last = p
		}
	}
	n.pos = idx
	if !pic.valid() {
		pic = n.last
	}
	if !pic.valid() {
		return nil, fmt.Errorf("media: no decodable frame at %.3fs", t)
	}
	img := pic.rgba(n.vs.h >= 720)
	n.cache[idx] = img
	n.cacheQ = append(n.cacheQ, idx)
	for len(n.cacheQ) > 6 {
		delete(n.cache, n.cacheQ[0])
		n.cacheQ = n.cacheQ[1:]
	}
	return img, nil
}

// yuvToRGBA converts a 4:2:0 picture to RGBA with the limited-range BT.601
// (or BT.709 for HD) matrix. Large pictures are converted in horizontal bands
// on several goroutines.
func yuvToRGBA(src *image.YCbCr, bt709 bool) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// Fixed-point (16.16) coefficients.
	var cr2r, cb2g, cr2g, cb2b int32
	if bt709 {
		cr2r, cb2g, cr2g, cb2b = 117489, 13975, 34925, 138438
	} else {
		cr2r, cb2g, cr2g, cb2b = 104597, 25675, 53279, 132201
	}
	band := func(y0, y1 int) {
		const yscale = 76309 // 255/219 in 16.16
		for y := y0; y < y1; y++ {
			yr := src.Y[(y+b.Min.Y-src.Rect.Min.Y)*src.YStride+(b.Min.X-src.Rect.Min.X):]
			ci := src.COffset(b.Min.X, b.Min.Y+y)
			d := dst.Pix[y*dst.Stride : y*dst.Stride+4*w]
			cb, cr := src.Cb[ci:], src.Cr[ci:]
			for x := 0; x+1 < w; x += 2 {
				// two pixels share one chroma sample
				u := int32(cb[x>>1]) - 128
				v := int32(cr[x>>1]) - 128
				rc := cr2r*v + 32768
				gc := 32768 - cb2g*u - cr2g*v
				bc := cb2b*u + 32768
				y0 := (int32(yr[x]) - 16) * yscale
				y1 := (int32(yr[x+1]) - 16) * yscale
				o := d[4*x : 4*x+8 : 4*x+8]
				o[0], o[1], o[2], o[3] = clip8((y0+rc)>>16), clip8((y0+gc)>>16), clip8((y0+bc)>>16), 255
				o[4], o[5], o[6], o[7] = clip8((y1+rc)>>16), clip8((y1+gc)>>16), clip8((y1+bc)>>16), 255
			}
			if w&1 == 1 { // odd width: the last pixel
				x := w - 1
				u := int32(cb[x>>1]) - 128
				v := int32(cr[x>>1]) - 128
				yy := (int32(yr[x]) - 16) * yscale
				d[4*x], d[4*x+1], d[4*x+2], d[4*x+3] = clip8((yy+cr2r*v+32768)>>16), clip8((yy-cb2g*u-cr2g*v+32768)>>16), clip8((yy+cb2b*u+32768)>>16), 255
			}
		}
	}
	workers := min(runtime.GOMAXPROCS(0), 4)
	if w*h < 320*240 || workers < 2 {
		band(0, h)
		return dst
	}
	var wg sync.WaitGroup
	step := (h + workers - 1) / workers
	for y := 0; y < h; y += step {
		y1 := min(y+step, h)
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			band(y0, y1)
		}(y, y1)
	}
	wg.Wait()
	return dst
}

func clip8(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// vp9IsKey reports whether a VP9 frame is a (shown) key frame.
func vp9IsKey(d []byte) bool {
	if len(d) < 2 || d[0]>>6 != 2 {
		return false
	}
	profile := (d[0]>>5)&1 | ((d[0]>>4)&1)<<1
	bit := uint(3)
	if profile == 3 {
		bit = 2
	}
	if profile == 3 && d[0]>>2&1 != 0 {
		return false
	}
	if d[0]>>bit&1 != 0 { // show_existing_frame
		return false
	}
	bit--
	return d[0]>>bit&1 == 0
}
