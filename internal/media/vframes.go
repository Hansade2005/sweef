package media

import (
	"fmt"
	"image"
	"sort"
	"sync"

	"github.com/sweefbrowser/sweef/internal/media/vp8"
)

// vdecoder is a pure-Go video decoder fed one access unit at a time. Decode
// returns the picture decoded from that access unit (nil when the unit does
// not produce a displayable picture); pictures are never modified later.
type vdecoder interface {
	Decode(data []byte) (*image.YCbCr, error)
	Reset()
}

type vp8Adapter struct{ d *vp8.Decoder }

func (a *vp8Adapter) Decode(data []byte) (*image.YCbCr, error) { return a.d.Decode(data) }
func (a *vp8Adapter) Reset()                                   { a.d.Reset() }

// newNativeDecoder returns a decoder for the stream codec, or nil when no
// pure-Go decoder exists for it.
func newNativeDecoder(vs *vstream) vdecoder {
	switch vs.codec {
	case "vp8":
		return &vp8Adapter{vp8.NewDecoder()}
	}
	return nil
}

// HasNativeVideo reports whether codec (ffprobe style name) can be decoded
// without ffmpeg.
func HasNativeVideo(codec string) bool {
	switch codec {
	case "vp8":
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
	last   *image.YCbCr
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
		n.last = nil
	} else if k := n.keyBefore(idx); k > n.pos {
		start = k
		n.dec.Reset()
		n.last = nil
	}
	var pic *image.YCbCr
	for i := start; i <= idx; i++ {
		data, err := n.vs.payload(n.src, i)
		if err != nil {
			n.pos = -1
			return nil, err
		}
		p, err := n.dec.Decode(data)
		if err != nil {
			// A broken access unit: keep going so later key frames recover.
			if i == idx && n.last == nil {
				n.pos = -1
				return nil, fmt.Errorf("media: %s decode: %w", n.vs.codec, err)
			}
			continue
		}
		pic = p
		if p != nil {
			n.last = p
		}
	}
	n.pos = idx
	if pic == nil {
		pic = n.last
	}
	if pic == nil {
		return nil, fmt.Errorf("media: no decodable frame at %.3fs", t)
	}
	img := yuvToRGBA(pic, n.vs.h >= 720)
	n.cache[idx] = img
	n.cacheQ = append(n.cacheQ, idx)
	for len(n.cacheQ) > 6 {
		delete(n.cache, n.cacheQ[0])
		n.cacheQ = n.cacheQ[1:]
	}
	return img, nil
}

// yuvToRGBA converts a 4:2:0 picture to RGBA with the limited-range BT.601
// (or BT.709 for HD) matrix.
func yuvToRGBA(src *image.YCbCr, bt709 bool) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// Fixed-point (16.16) coefficients.
	var cr2r, cb2g, cr2g, cb2b int32
	if bt709 {
		cr2r, cb2g, cr2g, cb2b = 103206, 12276, 30679, 121609
	} else {
		cr2r, cb2g, cr2g, cb2b = 91881, 22554, 46802, 116130
	}
	const yscale = 76309 // 255/219 in 16.16
	for y := 0; y < h; y++ {
		yr := src.Y[(y+b.Min.Y-src.Rect.Min.Y)*src.YStride+(b.Min.X-src.Rect.Min.X):]
		ci := src.COffset(b.Min.X, b.Min.Y+y)
		d := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			yy := (int32(yr[x]) - 16) * yscale
			cb := int32(src.Cb[ci+x>>1]) - 128
			cr := int32(src.Cr[ci+x>>1]) - 128
			r := (yy + cr2r*cr + 32768) >> 16
			g := (yy - cb2g*cb - cr2g*cr + 32768) >> 16
			bl := (yy + cb2b*cb + 32768) >> 16
			d[4*x] = clip8(r)
			d[4*x+1] = clip8(g)
			d[4*x+2] = clip8(bl)
			d[4*x+3] = 255
		}
	}
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
